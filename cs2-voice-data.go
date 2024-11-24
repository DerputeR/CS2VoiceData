package main

import (
	"CS2VoiceData/decoder"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	dem "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs"
	"github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs/msgs2"
)

func main() {
	outDir := "output"
	os.MkdirAll(outDir, 0755)

	// Create a map of a users to voice data.
	// Each chunk of voice data is a slice of bytes, store all those slices in a grouped slice.
	var voiceDataPerPlayer = map[string][][]byte{}

	// The file path to an unzipped demo file.
	file, err := os.Open("1-34428882-6181-4c75-a24b-4982764122e2.dem")
	if err != nil {
		log.Fatal("Failed to open demo file")
	}
	defer file.Close()

	parser := dem.NewParser(file)
	var format string

	parser.RegisterNetMessageHandler(func(m *msgs2.CNETMsg_Tick) {
		fmt.Printf("SERVER: Tick 010%d (time: %f)\n", *m.Tick, parser.CurrentTime().Seconds())
	})

	parser.RegisterNetMessageHandler(func(m *msgs2.CDemoFileHeader) {
		fmt.Println(m.String())
	})

	// never gets called :(
	parser.RegisterNetMessageHandler(func(m *msgs2.CDemoFileInfo) {
		fmt.Printf("Round start ticks: %v\n", m.GameInfo.Cs.GetRoundStartTicks())
		fmt.Printf("Frames=%d, Ticks=%d\n", m.GetPlaybackFrames(), m.GetPlaybackTicks())
	})

	parser.RegisterNetMessageHandler(func(m *msgs2.CSVCMsg_ServerInfo) {
		fmt.Println(m.String())
	})

	// Add a parser register for the VoiceData net message.
	parser.RegisterNetMessageHandler(func(m *msgs2.CSVCMsg_VoiceData) {
		// print tick. m.Tick/GetTick always return 0 for this net msg
		fmt.Printf("Demo Frame %d (SteamID=%d, Mask=%d, Client=%d, Proximity=%t, Passthru=%d)\n",
			parser.CurrentFrame(), m.GetXuid(), m.GetAudibleMask(), m.GetClient(), m.GetProximity(), m.GetPassthrough())
		fmt.Printf("Audio (%s): NumPackets=%d, len(VoiceData)=%d, SeqBytes=%d, SecNum=%d, SampleRate=%d, UncompOffset=%d, PacketsOffset=%v, VoiceLvl=%f\n",
			m.Audio.Format.String(), m.Audio.GetNumPackets(), len(m.Audio.VoiceData), m.Audio.GetSequenceBytes(), m.Audio.GetSectionNumber(), m.Audio.GetSampleRate(), m.Audio.GetUncompressedSampleOffset(), m.Audio.GetPacketOffsets(), m.Audio.GetVoiceLevel())

		// Get the users Steam ID 64.
		steamId := strconv.Itoa(int(m.GetXuid()))
		// Append voice data to map
		format = m.Audio.Format.String()
		voiceDataPerPlayer[steamId] = append(voiceDataPerPlayer[steamId], m.Audio.VoiceData)
	})

	// ParseHeader is deprecated (see https://github.com/markus-wa/demoinfocs-golang/discussions/568)
	// header, err := parser.ParseHeader()
	// fmt.Printf("Demo framerate: %f (Server tickrate: 64)\n", header.FrameRate())

	// Parse the full demo file.
	// err = parser.ParseToEnd()
	var moreFrames bool = true
	for moreFrames {
		moreFrames, err = parser.ParseNextFrame()
		fmt.Printf("PARSER: Frame %010d (Tick %010d, %f)\n---------------------\n", parser.CurrentFrame(), parser.GameState().IngameTick(), parser.CurrentTime().Seconds())
	}

	// For each users data, create a wav file containing their voice comms.
	for playerId, voiceData := range voiceDataPerPlayer {
		wavFilePath := fmt.Sprintf("%s/%s.wav", outDir, playerId)
		if format == "VOICEDATA_FORMAT_OPUS" {
			err = opusToWav(voiceData, wavFilePath)
			if err != nil {
				fmt.Println(err)
				continue
			}

		} else if format == "VOICEDATA_FORMAT_STEAM" {
			convertAudioDataToWavFiles(voiceData, wavFilePath)
		}
	}

	defer parser.Close()
}

func convertAudioDataToWavFiles(payloads [][]byte, fileName string) {
	// This sample rate can be set using data from the VoiceData net message.
	// But every demo processed has used 24000 and is single channel.
	voiceDecoder, err := decoder.NewOpusDecoder(24000, 1)

	if err != nil {
		fmt.Println(err)
	}

	o := make([]int, 0, 1024)

	for _, payload := range payloads {
		c, err := decoder.DecodeChunk(payload)

		if err != nil {
			fmt.Println(err)
		}

		// Not silent frame
		if c != nil && len(c.Data) > 0 {
			pcm, err := voiceDecoder.Decode(c.Data)

			if err != nil {
				fmt.Println(err)
			}

			converted := make([]int, len(pcm))
			for i, v := range pcm {
				// Float32 buffer implementation is wrong in go-audio, so we have to convert to int before encoding
				converted[i] = int(v * 2147483647)
			}

			o = append(o, converted...)
		}
	}

	outFile, err := os.Create(fileName)

	if err != nil {
		fmt.Println(err)
	}
	defer outFile.Close()

	// Encode new wav file, from decoded opus data.
	enc := wav.NewEncoder(outFile, 24000, 32, 1, 1)

	buf := &audio.IntBuffer{
		Data: o,
		Format: &audio.Format{
			SampleRate:  24000,
			NumChannels: 1,
		},
	}

	// Write voice data to the file.
	if err := enc.Write(buf); err != nil {
		fmt.Println(err)
	}

	enc.Close()
}

func opusToWav(data [][]byte, wavName string) (err error) {
	opusDecoder, err := decoder.NewDecoder(48000, 1)
	if err != nil {
		return
	}

	var pcmBuffer []int

	for _, d := range data {
		pcm, err := decoder.Decode(opusDecoder, d)
		if err != nil {
			log.Println(err)
			continue
		}

		pp := make([]int, len(pcm))

		for i, p := range pcm {
			pp[i] = int(p * 2147483647)
		}

		pcmBuffer = append(pcmBuffer, pp...)
	}

	file, err := os.Create(wavName)
	if err != nil {
		return
	}
	defer file.Close()

	enc := wav.NewEncoder(file, 48000, 32, 1, 1)
	defer enc.Close()

	buffer := &audio.IntBuffer{
		Data: pcmBuffer,
		Format: &audio.Format{
			SampleRate:  48000,
			NumChannels: 1,
		},
	}

	err = enc.Write(buffer)
	if err != nil {
		return
	}

	return
}
