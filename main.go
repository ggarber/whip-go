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
	if len(os.Args) < 3 {
		log.Fatal("Invalid number of arguments, pass endpoint url (and optionally a token)")
	}

	//get the media here
	// configure codec specific parameters
	vpxParams, _ := vpx.NewVP8Params()
	vpxParams.BitRate = 1_000_000 // 1mbps

	codecSelector := mediadevices.NewCodecSelector(
		mediadevices.WithVideoEncoders(&vpxParams),
	)

	stream, err := mediadevices.GetDisplayMedia(mediadevices.MediaStreamConstraints{
		Video: func(constraint *mediadevices.MediaTrackConstraints) {},
		Codec: codecSelector,
	})
	if err != nil {
		log.Fatal("Unexpected error capturing screen. ", err)
	}

	mediaEngine := webrtc.MediaEngine{}
	codecSelector.Populate(&mediaEngine)
	// mediaEngine.RegisterDefaultCodecs()

	//pass it into the whip client
	iceServers := []webrtc.ICEServer{}

	whip := NewWHIPClient(os.Args[1], os.Args[2])
	whip.Publish(stream, mediaEngine, iceServers, false)

	fmt.Print("Press 'Enter' to continue...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')

	whip.Close()
}
