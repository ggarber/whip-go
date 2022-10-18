package main

import (
	"bufio"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/interceptor"
	"github.com/pion/mediadevices"
	"github.com/pion/mediadevices/pkg/codec"
	"github.com/pion/mediadevices/pkg/io/video"
	"github.com/pion/mediadevices/pkg/prop"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

func GetInputMediaStream(codecSelector *CodecSelector) (mediadevices.MediaStream, error) {
	tracks := make([]mediadevices.Track, 0)

	track, err := GetInputMediaTrack(codecSelector)
	if err != nil {
		return nil, err
	}

	tracks = append(tracks, track)

	stream, err := mediadevices.NewMediaStream(tracks...)
	if err != nil {
		return nil, err
	}

	return stream, nil
}

func GetInputMediaTrack(codecSelector *CodecSelector) (mediadevices.Track, error) {
	stdin := bufio.NewReader(os.Stdin)

	reader := video.ReaderFunc(func() (img image.Image, release func(), err error) {
		area := 1280 * 720
		bytes := make([]byte, 1280*720*1.5)
		n, err := stdin.Read(bytes)
		println("read", n, "bytes")
		yuv := image.NewYCbCr(image.Rect(0, 0, 1280, 720), image.YCbCrSubsampleRatio420)
		copy(yuv.Y, bytes[0:area])
		copy(yuv.Cr, bytes[area:area+area/4])
		copy(yuv.Cb, bytes[area+area/4:area+area/4+area/4])
		img = yuv
		return img, func() {}, nil
	})
	track := newVideoTrackFromReader(reader, codecSelector)
	return track, nil
}

type baseTrack struct {
	mediadevices.Source
	err                   error
	onErrorHandler        func(error)
	mu                    sync.Mutex
	endOnce               sync.Once
	kind                  mediadevices.MediaDeviceType
	selector              *CodecSelector
	activePeerConnections map[string]chan<- chan<- struct{}
}

func newBaseTrack(kind mediadevices.MediaDeviceType, selector *CodecSelector) *baseTrack {
	return &baseTrack{
		Source:                NewSource(),
		kind:                  kind,
		selector:              selector,
		activePeerConnections: make(map[string]chan<- chan<- struct{}),
	}
}

type InputSource struct {
}

func NewSource() InputSource {
	return InputSource{}
}

func (source InputSource) Close() error {
	return nil
}

func (source InputSource) ID() string {
	generator, err := uuid.NewRandom()
	if err != nil {
		panic(err)
	}

	return generator.String()
}

type VideoTrack struct {
	*baseTrack
	*video.Broadcaster
	shouldCopyFrames bool
}

const (
	rtpOutboundMTU = 1200
	rtcpInboundMTU = 1500
)

// Kind returns track's kind
func (track *baseTrack) Kind() webrtc.RTPCodecType {
	switch track.kind {
	case mediadevices.VideoInput:
		return webrtc.RTPCodecTypeVideo
	case mediadevices.AudioInput:
		return webrtc.RTPCodecTypeAudio
	default:
		panic("invalid track kind: only support VideoInput and AudioInput")
	}
}

func (track *baseTrack) StreamID() string {
	// TODO: StreamID should be used to group multiple tracks. Should get this information from mediastream instead.
	generator, err := uuid.NewRandom()
	if err != nil {
		panic(err)
	}

	return generator.String()
}

// RID is only relevant if you wish to use Simulcast
func (track *baseTrack) RID() string {
	return ""
}

// OnEnded sets an error handler. When a track has been created and started, if an
// error occurs, handler will get called with the error given to the parameter.
func (track *baseTrack) OnEnded(handler func(error)) {
	track.mu.Lock()
	track.onErrorHandler = handler
	err := track.err
	track.mu.Unlock()

	if err != nil && handler != nil {
		// Already errored.
		track.endOnce.Do(func() {
			handler(err)
		})
	}
}

// onError is a callback when an error occurs
func (track *baseTrack) onError(err error) {
	track.mu.Lock()
	track.err = err
	handler := track.onErrorHandler
	track.mu.Unlock()

	if handler != nil {
		track.endOnce.Do(func() {
			handler(err)
		})
	}
}

func (track *VideoTrack) Bind(ctx webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	return track.bind(ctx, track)
}

func (track *VideoTrack) Unbind(ctx webrtc.TrackLocalContext) error {
	return track.unbind(ctx)
}

func (track *baseTrack) bind(ctx webrtc.TrackLocalContext, specializedTrack mediadevices.Track) (webrtc.RTPCodecParameters, error) {
	track.mu.Lock()
	defer track.mu.Unlock()

	signalCh := make(chan chan<- struct{})
	var stopRead chan struct{}
	track.activePeerConnections[ctx.ID()] = signalCh

	var encodedReader mediadevices.RTPReadCloser
	var selectedCodec webrtc.RTPCodecParameters
	var err error
	var errReasons []string
	for _, wantedCodec := range ctx.CodecParameters() {
		// logger.Debugf("trying to build %s rtp reader", wantedCodec.MimeType)
		encodedReader, err = specializedTrack.NewRTPReader(wantedCodec.MimeType, uint32(ctx.SSRC()), rtpOutboundMTU)
		if err == nil {
			selectedCodec = wantedCodec
			break
		}

		errReasons = append(errReasons, fmt.Sprintf("%s: %s", wantedCodec.MimeType, err))
	}

	if encodedReader == nil {
		return webrtc.RTPCodecParameters{}, errors.New(strings.Join(errReasons, "\n\n"))
	}

	go func() {
		var doneCh chan<- struct{}
		writer := ctx.WriteStream()
		defer func() {
			close(stopRead)
			encodedReader.Close()

			// When there's another call to unbind, it won't block since we remove the current ctx from active connections
			track.removeActivePeerConnection(ctx.ID())
			close(signalCh)
			if doneCh != nil {
				close(doneCh)
			}
		}()

		for {
			select {
			case doneCh = <-signalCh:
				return
			default:
			}

			pkts, _, err := encodedReader.Read()
			if err != nil {
				// explicitly ignore this error since the higher level should've reported this
				return
			}

			for _, pkt := range pkts {
				_, err = writer.WriteRTP(&pkt.Header, pkt.Payload)
				if err != nil {
					track.onError(err)
					return
				}
			}
		}
	}()

	keyFrameController, ok := encodedReader.Controller().(codec.KeyFrameController)
	if ok {
		stopRead = make(chan struct{})
		go track.rtcpReadLoop(ctx.RTCPReader(), keyFrameController, stopRead)
	}

	return selectedCodec, nil
}

func (track *baseTrack) rtcpReadLoop(reader interceptor.RTCPReader, keyFrameController codec.KeyFrameController, stopRead chan struct{}) {
	readerBuffer := make([]byte, rtcpInboundMTU)

readLoop:
	for {
		select {
		case <-stopRead:
			return
		default:
		}

		readLength, _, err := reader.Read(readerBuffer, interceptor.Attributes{})
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			// logger.Warnf("failed to read rtcp packet: %s", err)
			continue
		}

		pkts, err := rtcp.Unmarshal(readerBuffer[:readLength])
		if err != nil {
			// logger.Warnf("failed to unmarshal rtcp packet: %s", err)
			continue
		}

		for _, pkt := range pkts {
			switch pkt.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				if err := keyFrameController.ForceKeyFrame(); err != nil {
					// logger.Warnf("failed to force key frame: %s", err)
					continue readLoop
				}
			}
		}
	}
}

func (track *baseTrack) unbind(ctx webrtc.TrackLocalContext) error {
	ch := track.removeActivePeerConnection(ctx.ID())
	// If there isn't a registered chanel for this ctx, it means it has already been unbound
	if ch == nil {
		return nil
	}

	doneCh := make(chan struct{})
	ch <- doneCh
	<-doneCh
	return nil
}

func (track *baseTrack) removeActivePeerConnection(id string) chan<- chan<- struct{} {
	track.mu.Lock()
	defer track.mu.Unlock()

	ch, ok := track.activePeerConnections[id]
	if !ok {
		return nil
	}
	delete(track.activePeerConnections, id)

	return ch
}

func detectCurrentVideoProp(broadcaster *video.Broadcaster) (prop.Media, error) {
	var currentProp prop.Media

	// Since broadcaster has a ring buffer internally, a new reader will either read the last
	// buffered frame or a new frame from the source. This also implies that no frame will be lost
	// in any case.
	metaReader := broadcaster.NewReader(false)
	metaReader = video.DetectChanges(0, 0, func(p prop.Media) { currentProp = p })(metaReader)
	_, _, err := metaReader.Read()

	return currentProp, err
}

type samplerFunc func() uint32

// newVideoSampler creates a video sampler that uses the actual video frame rate and
// the codec's clock rate to come up with a duration for each sample.
func newVideoSampler(clockRate uint32) samplerFunc {
	clockRateFloat := float64(clockRate)
	lastTimestamp := time.Now()

	return samplerFunc(func() uint32 {
		now := time.Now()
		duration := now.Sub(lastTimestamp).Seconds()
		samples := uint32(math.Round(clockRateFloat * duration))
		lastTimestamp = now
		return samples
	})
}

func (track *VideoTrack) newEncodedReader(codecNames ...string) (mediadevices.EncodedReadCloser, *codec.RTPCodec, error) {
	reader := track.NewReader(track.shouldCopyFrames)
	inputProp, err := detectCurrentVideoProp(track.Broadcaster)
	if err != nil {
		return nil, nil, err
	}

	encodedReader, selectedCodec, err := selectVideoCodecByNames(track.selector, reader, inputProp, codecNames...)
	if err != nil {
		return nil, nil, err
	}

	sample := newVideoSampler(selectedCodec.ClockRate)

	return &encodedReadCloserImpl{
		readFn: func() (mediadevices.EncodedBuffer, func(), error) {
			data, release, err := encodedReader.Read()
			buffer := mediadevices.EncodedBuffer{
				Data:    data,
				Samples: sample(),
			}
			return buffer, release, err
		},
		closeFn:      encodedReader.Close,
		controllerFn: encodedReader.Controller,
	}, selectedCodec, nil
}

func (track *VideoTrack) NewEncodedReader(codecName string) (mediadevices.EncodedReadCloser, error) {
	reader, _, err := track.newEncodedReader(codecName)
	return reader, err
}

func (track *VideoTrack) NewEncodedIOReader(codecName string) (io.ReadCloser, error) {
	panic("not implemented NewEncodedIOReader")
}

func (track *VideoTrack) NewRTPReader(codecName string, ssrc uint32, mtu int) (mediadevices.RTPReadCloser, error) {
	encodedReader, selectedCodec, err := track.newEncodedReader(codecName)
	if err != nil {
		return nil, err
	}

	packetizer := rtp.NewPacketizer(uint16(mtu), uint8(selectedCodec.PayloadType), ssrc, selectedCodec.Payloader, rtp.NewRandomSequencer(), selectedCodec.ClockRate)

	return &rtpReadCloserImpl{
		readFn: func() ([]*rtp.Packet, func(), error) {
			encoded, release, err := encodedReader.Read()
			if err != nil {
				encodedReader.Close()
				track.onError(err)
				return nil, func() {}, err
			}
			defer release()

			pkts := packetizer.Packetize(encoded.Data, encoded.Samples)
			return pkts, release, err
		},
		closeFn:      encodedReader.Close,
		controllerFn: encodedReader.Controller,
	}, nil
}

func newVideoTrackFromReader(reader video.Reader, selector *CodecSelector) mediadevices.Track {
	base := newBaseTrack(mediadevices.VideoInput, selector)
	wrappedReader := video.ReaderFunc(func() (img image.Image, release func(), err error) {
		img, _, err = reader.Read()
		if err != nil {
			// base.onError(err)
		}
		return img, func() {}, err
	})

	// TODO: Allow users to configure broadcaster
	broadcaster := video.NewBroadcaster(wrappedReader, nil)

	return &VideoTrack{
		baseTrack:   base,
		Broadcaster: broadcaster,
	}
}

// rtpreader.go

type rtpReadCloserImpl struct {
	readFn       func() ([]*rtp.Packet, func(), error)
	closeFn      func() error
	controllerFn func() codec.EncoderController
}

func (r *rtpReadCloserImpl) Read() ([]*rtp.Packet, func(), error) {
	return r.readFn()
}

func (r *rtpReadCloserImpl) Close() error {
	return r.closeFn()
}

func (r *rtpReadCloserImpl) Controller() codec.EncoderController {
	return r.controllerFn()
}

// ioreader.go

type encodedReadCloserImpl struct {
	readFn       func() (mediadevices.EncodedBuffer, func(), error)
	closeFn      func() error
	controllerFn func() codec.EncoderController
}

func (r *encodedReadCloserImpl) Read() (mediadevices.EncodedBuffer, func(), error) {
	return r.readFn()
}

func (r *encodedReadCloserImpl) Close() error {
	return r.closeFn()
}

func (r *encodedReadCloserImpl) Controller() codec.EncoderController {
	return r.controllerFn()
}

type encodedIOReadCloserImpl struct {
	readFn     func([]byte) (int, error)
	closeFn    func() error
	controller func() codec.EncoderController
}

func newEncodedIOReadCloserImpl(reader mediadevices.EncodedReadCloser) *encodedIOReadCloserImpl {
	var encoded mediadevices.EncodedBuffer
	release := func() {}
	return &encodedIOReadCloserImpl{
		readFn: func(b []byte) (int, error) {
			var err error

			if len(encoded.Data) == 0 {
				release()
				encoded, release, err = reader.Read()
				if err != nil {
					reader.Close()
					return 0, err
				}
			}

			n := copy(b, encoded.Data)
			encoded.Data = encoded.Data[n:]
			return n, nil
		},
		closeFn:    reader.Close,
		controller: reader.Controller,
	}
}

func (r *encodedIOReadCloserImpl) Read(b []byte) (int, error) {
	return r.readFn(b)
}

func (r *encodedIOReadCloserImpl) Close() error {
	return r.closeFn()
}

func (r *encodedIOReadCloserImpl) Controller() codec.EncoderController {
	return r.controller()
}

// codec.go

func selectVideoCodecByNames(selector *CodecSelector, reader video.Reader, inputProp prop.Media, codecNames ...string) (codec.ReadCloser, *codec.RTPCodec, error) {
	var selectedEncoder codec.VideoEncoderBuilder
	var encodedReader codec.ReadCloser
	var errReasons []string
	var err error

outer:
	for _, wantCodec := range codecNames {
		wantCodecLower := strings.ToLower(wantCodec)
		for _, encoder := range selector.videoEncoders {
			// MimeType is formated as "video/<codecName>"
			if strings.HasSuffix(strings.ToLower(encoder.RTPCodec().MimeType), wantCodecLower) {
				encodedReader, err = encoder.BuildVideoEncoder(reader, inputProp)
				if err == nil {
					selectedEncoder = encoder
					break outer
				}
			}

			errReasons = append(errReasons, fmt.Sprintf("%s: %s", encoder.RTPCodec().MimeType, err))
		}
	}

	if selectedEncoder == nil {
		return nil, nil, errors.New(strings.Join(errReasons, "\n\n"))
	}

	return encodedReader, selectedEncoder.RTPCodec(), nil
}
