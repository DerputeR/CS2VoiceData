package main

import (
	"CS2VoiceData/decoder"
	"bufio"
	"errors"
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

type FramedSlice[T any] struct {
	id    uint64
	frame int
	data  []T
}

type FrameAudioInfo struct {
	id      uint64
	frame   int
	samples int
}

// default is 750 = 48000 hz / 64 fps
var samplesPerFrame int = 750

var logLevel slog.Level = slog.LevelInfo

func main() {
	startTime := time.Now()

	outDir := "output"
	os.MkdirAll(outDir, 0755)

	var err error
	logFile, err := os.Create("log.log")
	if err != nil {
		logFile = os.Stdout
	}
	// default buffer size is 4096
	// 32768 seems optimal for my machine when log level is INFO
	bufSize := 4096
	if len(os.Args) == 2 {
		bufSize, err = strconv.Atoi(os.Args[1])
		if err != nil {
			bufSize = 4096
		}
	}
	logWriter := bufio.NewWriterSize(logFile, bufSize)
	defer func() {
		logWriter.Flush()
		logFile.Close()
	}()

	logger := slog.New(slog.NewJSONHandler(logWriter, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == "time" {
				return slog.Attr{}
			}
			return slog.Attr{Key: a.Key, Value: a.Value}
		},
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	// Create a map of a user's steamid64 to voice data.
	var rawAudioMap = map[uint64][]FramedSlice[byte]{}

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
		// Print tick. m.Tick/GetTick always return 0 for this net msg
		slog.Info("CSVCMsg_VoiceData", "Frame", parser.CurrentFrame(), "SteamID", m.GetXuid(), "Mask", m.GetAudibleMask(), "Client", m.GetClient(), "Proximity", m.GetProximity(), "Passthrough", m.GetPassthrough(), slog.Group("Audio", "NumPackets", m.Audio.GetNumPackets(), "BytesEncoded", len(m.Audio.GetVoiceData()), "SequenceBytes", m.Audio.GetSequenceBytes(), "SectionNumber", m.Audio.GetSectionNumber(), "SampleRate", m.Audio.GetSampleRate(), "UncompressedSampleOffset", m.Audio.GetUncompressedSampleOffset(), "PacketOffsets", m.Audio.GetPacketOffsets(), "VoiceLevel", m.Audio.GetVoiceLevel()))

		// Get the users Steam ID 64.
		steamId := m.GetXuid()

		// Append voice data to map
		// todo: find out of it's possible this can be different per player
		format = m.Audio.Format.String()
		// todo: find out if we should sync against server tick (parser.GameState().IngameTick()) or against demo frame
		currentFrame := parser.CurrentFrame()
		// currentFrame := parser.GameState().IngameTick()
		rawAudioMap[steamId] = append(rawAudioMap[steamId], FramedSlice[byte]{id: steamId, frame: currentFrame, data: m.Audio.VoiceData})
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
	for playerId, voiceData := range rawAudioMap {
		wavFilePath := fmt.Sprintf("%s/%d.wav", outDir, playerId)
		if format == "VOICEDATA_FORMAT_OPUS" {
			pcm, err := opusToPcm(rawAudioMap[playerId])
			if err != nil {
				slog.Warn(err.Error())
				continue
			}

			err = writePcmToFile(pcm, wavFilePath)
			if err != nil {
				slog.Warn(err.Error())
				continue
			}

			// err = opusToWav(voiceData, wavFilePath)
			// if err != nil {
			// 	slog.Warn(err.Error())
			// 	continue
			// }

		} else if format == "VOICEDATA_FORMAT_STEAM" {
			// TODO
			slog.Warn("WARNING: Steam Audio has not been updated to add silence.")
			var voiceBytes [][]byte
			for _, d := range voiceData {
				voiceBytes = append(voiceBytes, d.data)
			}
			convertAudioDataToWavFiles(voiceBytes, wavFilePath)
		} else {
			slog.Warn("WARNING: Unknown audio encoding", "steamid", playerId, "format", format)
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

// todo
func opusToPcm(frameData []FramedSlice[byte]) (pcmData []FramedSlice[int], err error) {
	opusDecoder, err := decoder.NewDecoder(48000, 1)
	if err != nil {
		return
	}

	latestFrameAudioInfo := FrameAudioInfo{frame: 0, samples: 0}
	var longestFrameDuration int = 0

	for _, d := range frameData {

		pcm, err := decoder.Decode(opusDecoder, d.data)
		if err != nil {
			slog.Warn(err.Error())
			continue
		}

		// ? should this be int32
		pcmIntBuf := make([]int, len(pcm))

		for i, p := range pcm {
			// todo: use the go-audio transforms instead of manually mangling the samples up to int samples
			pcmIntBuf[i] = int(p * 2147483647)
		}

		fDiff := d.frame - latestFrameAudioInfo.frame
		longestFrameDuration = max(longestFrameDuration, latestFrameAudioInfo.samples)
		if fDiff == 0 {
			latestFrameAudioInfo.samples += len(pcmIntBuf)
		} else if fDiff == 1 {
			slog.Info("Frame stats", "Frame", latestFrameAudioInfo.frame, "Samples", latestFrameAudioInfo.samples, "steamid", d.id)
			latestFrameAudioInfo.frame = d.frame
			latestFrameAudioInfo.samples = len(pcmIntBuf)

		} else if fDiff > 1 {
			slog.Info("Frame stats", "Frame", latestFrameAudioInfo.frame, "Samples", latestFrameAudioInfo.samples, "steamid", d.id)
			// todo: insert silence
			// this is just temporary
			calculatedSilence := max(0, (samplesPerFrame-latestFrameAudioInfo.samples)+(samplesPerFrame*(fDiff-1)))

			// pcmData = append(pcmData, FramedSlice[int]{id: d.id, frame: latestFrameAudioInfo.frame + 1, data: make([]int, 10)})

			// todo: figure out how to precisely calculate this. we can use ints for now
			slog.Info(fmt.Sprintf("Silence found between %d (len: %d samples) and %d - calculated duration: %d samples", latestFrameAudioInfo.frame, latestFrameAudioInfo.samples, d.frame, calculatedSilence), "steamid", d.id)

			latestFrameAudioInfo.frame = d.frame
			latestFrameAudioInfo.samples = len(pcmIntBuf)
		} else {
			// should not happen...
			slog.Error("Error: Demo audio packet out of order!", "steamid", d.id, "PacketFrame", d.frame, "PacketLen", len(d.data), "LastPacketFrame", latestFrameAudioInfo.frame, "LastPacketLen", latestFrameAudioInfo.samples)
			return pcmData, errors.New("demo audio packet out of order")
		}

		longestFrameDuration = max(longestFrameDuration, latestFrameAudioInfo.samples)
		pcmData = append(pcmData, FramedSlice[int]{d.id, d.frame, pcmIntBuf})
	}
	slog.Info("PCM info", "steamid", frameData[0].id, "LongestFrameDuration", longestFrameDuration)
	return
}

func writePcmToFile(pcmData []FramedSlice[int], wavName string) (err error) {
	file, err := os.Create(wavName)
	if err != nil {
		return
	}
	defer file.Close()

	enc := wav.NewEncoder(file, 48000, 32, 1, 1)
	defer enc.Close()

	var pcmBuffer []int
	for _, d := range pcmData {
		pcmBuffer = append(pcmBuffer, d.data...)
	}

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
