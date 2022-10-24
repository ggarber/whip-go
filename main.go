package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/pion/mediadevices"
	"github.com/pion/mediadevices/pkg/codec/opus"
	"github.com/pion/mediadevices/pkg/codec/vpx"
	_ "github.com/pion/mediadevices/pkg/driver/screen" // This is required to register screen adapter
	"github.com/pion/webrtc/v3"
)

func main() {
	video := flag.String("v", "screen", "input video device, can be \"screen\" or a named pipe")
	audio := flag.String("a", "", "input audio device, can be a named pipe")
	videoBitrate := flag.Int("b", 1_000_000, "video bitrate in bits per second")
	iceServer := flag.String("i", "stun:stun.l.google.com:19302", "ice server")
	token := flag.String("t", "", "publishing token")
	flag.Parse()

	if len(flag.Args()) != 1 {
		log.Fatal("Invalid number of arguments, pass the publishing url as the first argument")
	}

	mediaEngine := webrtc.MediaEngine{}
	whip := NewWHIPClient(flag.Args()[0], *token)

	// configure codec specific parameters
	vpxParams, _ := vpx.NewVP8Params()
	vpxParams.BitRate = *videoBitrate

	opusParams, _ := opus.NewParams()

	var stream mediadevices.MediaStream
	var err error

	if *video == "screen" {
		codecSelector := mediadevices.NewCodecSelector(
			mediadevices.WithVideoEncoders(&vpxParams),
		)
		codecSelector.Populate(&mediaEngine)

		stream, err = mediadevices.GetDisplayMedia(mediadevices.MediaStreamConstraints{
			Video: func(constraint *mediadevices.MediaTrackConstraints) {},
			Codec: codecSelector,
		})
		if err != nil {
			log.Fatal("Unexpected error capturing screen. ", err)
		}
	} else {
		codecSelector := NewCodecSelector(
			WithVideoEncoders(&vpxParams),
			WithAudioEncoders(&opusParams),
		)
		codecSelector.Populate(&mediaEngine)

		stream, err = GetInputMediaStream(*audio, *video, codecSelector)
		if err != nil {
			log.Fatal("Unexpected error capturing input pipe. ", err)
		}
	}

	iceServers := []webrtc.ICEServer{
		{
			URLs: []string{*iceServer},
		},
	}

	whip.Publish(stream, mediaEngine, iceServers, false)

	fmt.Println("Press 'Enter' to finish...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')

	whip.Close()
}
