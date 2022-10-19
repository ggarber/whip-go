package main

import (
	"bufio"
	"fmt"
	"log"
	"os"

	"github.com/pion/mediadevices"
	"github.com/pion/mediadevices/pkg/codec/vpx"
	_ "github.com/pion/mediadevices/pkg/driver/screen" // This is required to register screen adapter
	"github.com/pion/webrtc/v3"
)

func main() {
	if len(os.Args) < 4 {
		log.Fatal("Invalid number of arguments, pass input (f.e. \"screen\"), endpoint url (and optionally a token)")
	}

	mediaEngine := webrtc.MediaEngine{}
	whip := NewWHIPClient(os.Args[2], os.Args[3])

	// configure codec specific parameters
	vpxParams, _ := vpx.NewVP8Params()
	vpxParams.BitRate = 1_000_000 // 1mbps

	var stream mediadevices.MediaStream
	var err error

	if os.Args[1] == "screen" {
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
		)
		codecSelector.Populate(&mediaEngine)

		stream, err = GetInputMediaStream(os.Args[1], codecSelector)
		if err != nil {
			log.Fatal("Unexpected error capturing input pipe. ", err)
		}
	}

	// TODO: Make it configurable
	iceServers := []webrtc.ICEServer{
		{
			URLs: []string{"stun:stun.l.google.com:19302"},
		},
	}

	whip.Publish(stream, mediaEngine, iceServers, false)

	fmt.Println("Press 'Enter' to continue...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')

	whip.Close()
}
