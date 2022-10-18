package main

import (
	"bufio"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/pion/interceptor"
	"github.com/pion/mediadevices"
	"github.com/pion/mediadevices/pkg/io/video"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
)

func GetInputMediaStream() (mediadevices.MediaStream, error) {
	tracks := make([]mediadevices.Track, 0)

	track, err := GetInputMediaTrack()
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

func GetInputMediaTrack() (mediadevices.Track, error) {
	stdin := bufio.NewReader(os.Stdin)

	reader := video.ReaderFunc(func() (img image.Image, release func(), err error) {
		bytes := make([]byte, 1280*720*1.5)
		stdin.Read(bytes)
		//
		img, err = image.Decode(bytes)
		return
	})
	track := newVideoTrackFromReader(reader, nil)
	return track, nil
}

type baseTrack struct {
	mediadevices.Source
	err                   error
	onErrorHandler        func(error)
	mu                    sync.Mutex
	endOnce               sync.Once
	kind                  mediadevices.MediaDeviceType
	selector              *mediadevices.CodecSelector
	activePeerConnections map[string]chan<- chan<- struct{}
}

func newBaseTrack(kind mediadevices.MediaDeviceType, selector *mediadevices.CodecSelector) *baseTrack {
	return &baseTrack{
		kind:                  kind,
		selector:              selector,
		activePeerConnections: make(map[string]chan<- chan<- struct{}),
	}
}

type VideoTrack struct {
	*baseTrack
	*video.Broadcaster
	shouldCopyFrames bool
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

func newVideoTrackFromReader(reader video.Reader, selector *mediadevices.CodecSelector) mediadevices.Track {
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
