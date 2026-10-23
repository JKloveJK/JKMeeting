package transcode

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLadder(t *testing.T) {
	ladder, err := ParseLadder("32, 64,96")
	if err != nil {
		t.Fatalf("ParseLadder() error = %v", err)
	}
	if len(ladder) != 3 || ladder[0].Name != "32k" || ladder[2].BitrateKbps != 96 {
		t.Fatalf("ladder = %+v", ladder)
	}
	if LadderString(ladder) != "32,64,96" {
		t.Fatalf("LadderString() = %q", LadderString(ladder))
	}
	for _, raw := range []string{"", "64,32", "32,32", "abc", "4", "1000"} {
		if _, err := ParseLadder(raw); err == nil {
			t.Fatalf("ParseLadder(%q) error = nil, want error", raw)
		}
	}
}

func TestArgsMapsEveryVariant(t *testing.T) {
	ladder, _ := ParseLadder("32,64")
	args := strings.Join(FFmpeg{}.Args("in.audio", ladder), " ")
	for _, want := range []string{
		"-i in.audio", "-map 0:a -map 0:a -c:a aac", "-b:a:0 32k", "-b:a:1 64k", "-hls_time 6",
		"-hls_segment_type fmp4", "-var_stream_map a:0,name:32k a:1,name:64k", "%v/index.m3u8",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q: %s", want, args)
		}
	}
}

func TestRunReportsMissingBinary(t *testing.T) {
	ladder, _ := ParseLadder("32")
	err := FFmpeg{Path: "/nonexistent/ffmpeg"}.Run(context.Background(), "in", t.TempDir(), ladder)
	if !errors.Is(err, ErrFFmpegNotFound) {
		t.Fatalf("Run() error = %v, want ErrFFmpegNotFound", err)
	}
}

func TestRunWithLocalFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("本机没有 ffmpeg")
	}
	ladder, _ := ParseLadder("32,64")
	ffmpeg := FFmpeg{Path: "ffmpeg", Nice: 10, SegmentSeconds: 2}

	corrupted := filepath.Join(t.TempDir(), "corrupted.audio")
	if err := os.WriteFile(corrupted, []byte("not audio at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	var ffmpegErr *FFmpegError
	if err := ffmpeg.Run(context.Background(), corrupted, t.TempDir(), ladder); !errors.As(err, &ffmpegErr) {
		t.Fatalf("Run(corrupted) error = %v, want FFmpegError", err)
	}

	input := filepath.Join(t.TempDir(), "tone.wav")
	writeWAV(t, input, 16_000, 5*16_000)
	outDir := t.TempDir()
	if err := ffmpeg.Run(context.Background(), input, outDir, ladder); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	master, err := os.ReadFile(filepath.Join(outDir, "master.m3u8"))
	if err != nil {
		t.Fatalf("read master error = %v", err)
	}
	if strings.Count(string(master), "#EXT-X-STREAM-INF") != 2 || !strings.Contains(string(master), "64k/index.m3u8") {
		t.Fatalf("master playlist = %s", master)
	}
	for _, name := range []string{"32k/index.m3u8", "32k/init_0.mp4", "64k/seg_000.m4s"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func writeWAV(t *testing.T, path string, rate, frames int) {
	t.Helper()
	data := make([]byte, 44+frames*2)
	copy(data[0:], "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(36+frames*2))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], uint32(rate))
	binary.LittleEndian.PutUint32(data[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(frames*2))
	for i := 0; i < frames; i++ {
		sample := int16(8_000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
		binary.LittleEndian.PutUint16(data[44+2*i:], uint16(sample))
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
