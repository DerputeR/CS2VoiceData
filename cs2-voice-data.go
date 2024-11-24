package main

import (
	"CS2VoiceData/decoder"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	dem "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs"
	"github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs/msgs2"
)

func main() {
	startTime := time.Now()

	outDir := "output"
	os.MkdirAll(outDir, 0755)

	logFile, err := os.Create("log.log")
	if err != nil {
		slog.Error("Unable to create JSON log file!", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == "time" {
				return slog.Attr{}
			}
			return slog.Attr{Key: a.Key, Value: a.Value}
		},
		Level: slog.LevelWarn,
	}))
	slog.SetDefault(logger)

	// Create a map of a users to voice data.
	// Each chunk of voice data is a slice of bytes, store all those slices in a grouped slice.
	var voiceDataPerPlayer = map[string][][]byte{}

	// The file path to an unzipped demo file.
	file, err := os.Open("1-34428882-6181-4c75-a24b-4982764122e2.dem")
	if err != nil {
		slog.Error("Failed to open demo file", "error", err)
		os.Exit(1)
	}
	defer file.Close()

	parser := dem.NewParser(file)
	var format string

	parser.RegisterNetMessageHandler(func(m *msgs2.CNETMsg_Tick) {
		slog.Debug(fmt.Sprintf("TICK: Tick %10d", m.Tick), "frame", parser.CurrentFrame(), "tick", parser.GameState().IngameTick(), "time", parser.CurrentTime().Seconds())
		// fmt.Printf("TICK: Tick %10d (frame: %10d, tick: %10d, time: %f)\n", *m.Tick, parser.CurrentFrame(), parser.GameState().IngameTick(), parser.CurrentTime().Seconds())
	})

	parser.RegisterNetMessageHandler(func(m *msgs2.CDemoFileHeader) {
		slog.Debug(m.String())
	})

	// Add a parser register for the VoiceData net message.
	parser.RegisterNetMessageHandler(func(m *msgs2.CSVCMsg_VoiceData) {
		// print tick. m.Tick/GetTick always return 0 for this net msg
		slog.Info("CSVCMsg_VoiceData", "SteamID", m.GetXuid(), "Mask", m.GetAudibleMask(), "Client", m.GetClient(), "Proximity", m.GetProximity(), "Passthrough", m.GetPassthrough(), slog.Group("Audio", "NumPackets", m.Audio.GetNumPackets(), "Samples", len(m.Audio.GetVoiceData()), "SequenceBytes", m.Audio.GetSequenceBytes(), "SectionNumber", m.Audio.GetSectionNumber(), "SampleRate", m.Audio.GetSampleRate(), "UncompressedSampleOffset", m.Audio.GetUncompressedSampleOffset(), "PacketOffsets", m.Audio.GetPacketOffsets(), "VoiceLevel", m.Audio.GetVoiceLevel()))

		// Get the users Steam ID 64.
		steamId := strconv.Itoa(int(m.GetXuid()))
		// Append voice data to map
		format = m.Audio.Format.String()
		voiceDataPerPlayer[steamId] = append(voiceDataPerPlayer[steamId], m.Audio.VoiceData)
	})

	// ParseHeader is deprecated (see https://github.com/markus-wa/demoinfocs-golang/discussions/568)
	// header, err := parser.ParseHeader()

	// Parse the full demo file.
	err = parser.ParseToEnd()
	// var moreFrames bool = true
	// for moreFrames {
	// 	moreFrames, err = parser.ParseNextFrame()
	// }
	elapsed := time.Since(startTime)
	slog.Warn(fmt.Sprintf("Time elapsed: %v", elapsed))

	// For each users data, create a wav file containing their voice comms.
	for playerId, voiceData := range voiceDataPerPlayer {
		wavFilePath := fmt.Sprintf("%s/%s.wav", outDir, playerId)
		if format == "VOICEDATA_FORMAT_OPUS" {
			err = opusToWav(voiceData, wavFilePath)
			if err != nil {
				slog.Warn(err.Error())
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
		slog.Warn(err.Error())
	}

	o := make([]int, 0, 1024)

	for _, payload := range payloads {
		c, err := decoder.DecodeChunk(payload)

		if err != nil {
			slog.Warn(err.Error())
		}

		// Not silent frame
		if c != nil && len(c.Data) > 0 {
			pcm, err := voiceDecoder.Decode(c.Data)

			if err != nil {
				slog.Warn(err.Error())
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
		slog.Warn(err.Error())
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
		slog.Warn(err.Error())
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
			slog.Warn(err.Error())
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
