package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/url"

	"github.com/pion/mediadevices"
	"github.com/pion/webrtc/v3"

	"github.com/pion/mediadevices/pkg/codec/vpx"
	_ "github.com/pion/mediadevices/pkg/driver/screen" // This is required to register screen adapter
)

type WHIPClient struct {
	endpoint    string
	token       string
	resourceUrl string
}

func NewWHIPClient(endpoint string, token string) *WHIPClient {
	client := new(WHIPClient)
	client.endpoint = endpoint
	client.token = token
	return client
}

func (whip *WHIPClient) Publish() {
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

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{},
	}
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(&mediaEngine)).NewPeerConnection(config)
	if err != nil {
		log.Fatal("Unexpected error building the PeerConnection. ", err)
	}

	for _, track := range stream.GetTracks() {
		track.OnEnded(func(err error) {
			log.Println("Track ended with error, ", err)
		})

		_, err = pc.AddTransceiverFromTrack(track,
			webrtc.RtpTransceiverInit{
				Direction: webrtc.RTPTransceiverDirectionSendonly,
			},
		)
		if err != nil {
			panic(err)
		}
	}

	pc.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Printf("PeerConnection State has changed %s \n", connectionState.String())
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Fatal("PeerConnection could not create offer. ", err)
	}
	err = pc.SetLocalDescription(offer)
	if err != nil {
		log.Fatal("PeerConnection could not set local offer. ", err)
	}

	// log.Println(offer.SDP)
	var sdp = []byte(offer.SDP)
	client := &http.Client{}
	req, err := http.NewRequest("POST", whip.endpoint, bytes.NewBuffer(sdp))
	if err != nil {
		log.Fatal("Unexpected error building http request. ", err)
	}

	req.Header.Add("Content-Type", "application/sdp")
	req.Header.Add("Authorization", "Bearer "+whip.token)

	resp, err := client.Do(req)
	if err != nil {
		log.Fatal("Failed http POST request. ", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)

	resourceUrl, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		log.Fatal("Failed to parse resource url. ", err)
	}
	base, err := url.Parse(whip.endpoint)
	if err != nil {
		log.Fatal("Failed to parse base url. ", err)
	}
	whip.resourceUrl = base.ResolveReference(resourceUrl).String()
	// log.Println(string(body))

	answer := webrtc.SessionDescription{}
	answer.Type = webrtc.SDPTypeAnswer
	answer.SDP = string(body)

	err = pc.SetRemoteDescription(answer)
	if err != nil {
		log.Fatal("PeerConnection could not set remote answer. ", err)
	}
}

func (whip *WHIPClient) Close() {
	req, err := http.NewRequest("DELETE", whip.resourceUrl, nil)
	if err != nil {
		log.Fatal("Unexpected error building http request. ", err)
	}
	req.Header.Add("Authorization", "Bearer "+whip.token)

	client := &http.Client{}
	_, err = client.Do(req)
	if err != nil {
		log.Fatal("Failed http DELETE request. ", err)
	}
}
