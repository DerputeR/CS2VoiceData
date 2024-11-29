package main

import (
	"CS2VoiceData/decoder"
	"bufio"
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
	time  time.Duration
	data  []T
}

type FrameAudioInfo struct {
	id      uint64
	frame   int
	time    time.Duration
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
		slog.Info("CSVCMsg_VoiceData", "Frame", parser.CurrentFrame(), "Time", parser.CurrentTime().String(), "SteamID", m.GetXuid(), "Mask", m.GetAudibleMask(), "Client", m.GetClient(), "Proximity", m.GetProximity(), "Passthrough", m.GetPassthrough(), slog.Group("Audio", "NumPackets", m.Audio.GetNumPackets(), "BytesEncoded", len(m.Audio.GetVoiceData()), "SequenceBytes", m.Audio.GetSequenceBytes(), "SectionNumber", m.Audio.GetSectionNumber(), "SampleRate", m.Audio.GetSampleRate(), "UncompressedSampleOffset", m.Audio.GetUncompressedSampleOffset(), "PacketOffsets", m.Audio.GetPacketOffsets(), "VoiceLevel", m.Audio.GetVoiceLevel()))

		// Get the users Steam ID 64.
		steamId := m.GetXuid()

		// Append voice data to map
		// todo: find out of it's possible this can be different per player
		format = m.Audio.Format.String()
		// todo: find out if we should sync against server tick (parser.GameState().IngameTick()) or against demo frame
		currentFrame := parser.CurrentFrame()
		currentTime := parser.CurrentTime()
		// currentFrame := parser.GameState().IngameTick()
		rawAudioMap[steamId] = append(rawAudioMap[steamId], FramedSlice[byte]{id: steamId, frame: currentFrame, time: currentTime, data: m.Audio.VoiceData})
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
	slog.Warn(fmt.Sprintf("Parsing took: %v", elapsed))

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

	var lastAudioChunkFrame int = 0
	var lastAudioChunkTimestamp time.Duration = 0
	var samplesAccumulated int = 0

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

		// todo: insert silence
		// TODO: if this works, convert it to using frame counts
		// time.Duration is in nanoseconds, and samplerate is in Hz (cycles per second).
		// 1/64 of a second is 0.015625 seconds, or 15,625,000 nanoseconds. It should be sufficient to just reference
		// microseconds, where 1 microsecond = 1000 nanoseconds
		// to convert Hz to cycles per nanosecond, divide Hz by 1,000,000,000. So 48,000 hz -> 0.000048 cycles/nanosec, or 0.048 cycles/microsecond
		msgTimeDiff := d.time - lastAudioChunkTimestamp
		samplesDuration := time.Duration(float64(samplesAccumulated) / (float64(48000) / float64(1000000000))) // in nanoseconds
		silenceDuration := msgTimeDiff - samplesDuration

		if silenceDuration > 0 {
			samplesNeeded := int64(float64(silenceDuration) * (float64(48000) / float64(1000000000)))
			slog.Info(fmt.Sprintf("Silence (%s) found between %d and %d - calculated duration: %d samples", silenceDuration.String(), lastAudioChunkFrame, d.frame, samplesNeeded), "steamid", d.id)
			pcmData = append(pcmData, FramedSlice[int]{id: d.id, frame: lastAudioChunkFrame + 1, data: make([]int, samplesNeeded)})
			lastAudioChunkTimestamp = d.time
			lastAudioChunkFrame = d.frame
			samplesAccumulated = len(d.data)
		}

		pcmData = append(pcmData, FramedSlice[int]{d.id, d.frame, d.time, pcmIntBuf})
	}
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
