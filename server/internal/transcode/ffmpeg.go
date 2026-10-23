package transcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ErrFFmpegNotFound 表示配置的 FFmpeg 不可执行；属于配置错误，重试无效。
var ErrFFmpegNotFound = errors.New("找不到 FFmpeg 可执行文件")

// FFmpegError 表示 FFmpeg 非零退出（输入损坏或参数不受支持），结果是确定性的，重试无效。
type FFmpegError struct {
	ExitCode int
	Stderr   string
}

func (err *FFmpegError) Error() string {
	return fmt.Sprintf("FFmpeg 退出码 %d：%s", err.ExitCode, err.Stderr)
}

type Variant struct {
	Name        string
	BitrateKbps int
}

// ParseLadder 解析 "32,64,96" 形式的码率阶梯（kbps），要求严格递增。
func ParseLadder(raw string) ([]Variant, error) {
	var ladder []Variant
	for _, part := range strings.Split(raw, ",") {
		kbps, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || kbps < 8 || kbps > 320 {
			return nil, fmt.Errorf("码率阶梯无效：%q", raw)
		}
		if len(ladder) > 0 && kbps <= ladder[len(ladder)-1].BitrateKbps {
			return nil, fmt.Errorf("码率阶梯须严格递增：%q", raw)
		}
		ladder = append(ladder, Variant{Name: strconv.Itoa(kbps) + "k", BitrateKbps: kbps})
	}
	return ladder, nil
}

func LadderString(ladder []Variant) string {
	parts := make([]string, len(ladder))
	for i, variant := range ladder {
		parts[i] = strconv.Itoa(variant.BitrateKbps)
	}
	return strings.Join(parts, ",")
}

type FFmpeg struct {
	Path           string
	Nice           int
	SegmentSeconds int
}

// Args 返回在输出目录内执行的 FFmpeg 参数（不含可执行文件本身）。
// 输出：master.m3u8、<name>/index.m3u8、<name>/init_<i>.mp4、<name>/seg_NNN.m4s。
func (ffmpeg FFmpeg) Args(input string, ladder []Variant) []string {
	args := []string{"-hide_banner", "-nostdin", "-v", "error", "-y", "-i", input}
	for range ladder {
		args = append(args, "-map", "0:a")
	}
	args = append(args, "-c:a", "aac", "-ac", "1", "-ar", "48000")
	streamMap := make([]string, len(ladder))
	for i, variant := range ladder {
		args = append(args, fmt.Sprintf("-b:a:%d", i), fmt.Sprintf("%dk", variant.BitrateKbps))
		streamMap[i] = fmt.Sprintf("a:%d,name:%s", i, variant.Name)
	}
	segment := ffmpeg.SegmentSeconds
	if segment <= 0 {
		segment = 6
	}
	return append(args,
		"-f", "hls", "-hls_time", strconv.Itoa(segment), "-hls_playlist_type", "vod",
		"-hls_segment_type", "fmp4", "-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", "%v/seg_%03d.m4s",
		"-master_pl_name", "master.m3u8",
		"-var_stream_map", strings.Join(streamMap, " "),
		"%v/index.m3u8",
	)
}

// Run 在 outDir 中执行转码；Nice > 0 时通过 nice 降低 CPU 优先级，让转写优先获得 CPU。
func (ffmpeg FFmpeg) Run(ctx context.Context, input, outDir string, ladder []Variant) error {
	path, err := exec.LookPath(ffmpeg.Path)
	if err != nil {
		return fmt.Errorf("%w：%s", ErrFFmpegNotFound, ffmpeg.Path)
	}
	args := ffmpeg.Args(input, ladder)
	command := exec.CommandContext(ctx, path, args...)
	if ffmpeg.Nice > 0 {
		command = exec.CommandContext(ctx, "nice", append([]string{"-n", strconv.Itoa(ffmpeg.Nice), path}, args...)...)
	}
	command.Dir = outDir
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &FFmpegError{ExitCode: exitErr.ExitCode(), Stderr: tail(stderr.String(), 200)}
		}
		return err
	}
	return nil
}

func tail(text string, limit int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[len(runes)-limit:])
}
