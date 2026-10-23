package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jiankailei/jkmeeting/server/internal/observability"
	"github.com/jiankailei/jkmeeting/server/internal/transcode"
)

const hlsTTL = 15 * time.Minute

type transcodeStore interface {
	FindTranscodeJob(context.Context, uuid.UUID) (transcode.Job, error)
}

type objectReader interface {
	ReadObject(context.Context, string, int64) ([]byte, error)
}

// HLSSigner 为一条录音的全部 HLS playlist 签发同一组 exp/sig，AVPlayer 无需携带 Authorization 头。
type HLSSigner struct {
	key []byte
	now func() time.Time
}

// NewHLSSigner 由 Bearer token 派生签名密钥，不新增密钥配置。
func NewHLSSigner(token string) *HLSSigner {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("jkmeeting-hls-v1"))
	return &HLSSigner{key: mac.Sum(nil), now: time.Now}
}

func (signer *HLSSigner) sign(recordingID uuid.UUID, exp int64) string {
	mac := hmac.New(sha256.New, signer.key)
	fmt.Fprintf(mac, "%s|%d", recordingID, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

func (signer *HLSSigner) Query(recordingID uuid.UUID) (string, time.Time) {
	expires := signer.now().Add(hlsTTL)
	exp := expires.Unix()
	return url.Values{"exp": {strconv.FormatInt(exp, 10)}, "sig": {signer.sign(recordingID, exp)}}.Encode(), expires
}

func (signer *HLSSigner) Verify(recordingID uuid.UUID, rawExp, sig string) bool {
	exp, err := strconv.ParseInt(rawExp, 10, 64)
	if err != nil || signer.now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(signer.sign(recordingID, exp)))
}

// WithHLS 启用 HLS 回放；未启用时回放接口只返回原始文件地址。
func (api *RecordingAPI) WithHLS(store transcodeStore, reader objectReader, signer *HLSSigner) *RecordingAPI {
	api.transcodes = store
	api.objectReader = reader
	api.hlsSigner = signer
	return api
}

// 只允许 master 与各档位的 index playlist，杜绝目录穿越和任意对象读取。
var hlsPlaylistPath = regexp.MustCompile(`^(master|[0-9]{1,3}k/index)\.m3u8$`)

// serveHLS 处理 GET /v1/hls/{id}/{playlist}?exp=&sig=，返回改写后的 playlist。
func (api *RecordingAPI) serveHLS(response http.ResponseWriter, request *http.Request, tc observability.TraceContext) {
	rest := strings.TrimPrefix(request.URL.Path, "/v1/hls/")
	idText, playlist, found := strings.Cut(rest, "/")
	id, err := uuid.Parse(idText)
	if !found || err != nil || !hlsPlaylistPath.MatchString(playlist) {
		api.writeError(response, tc.RequestID, http.StatusBadRequest, "HLS 路径无效")
		return
	}
	query := request.URL.Query()
	if api.hlsSigner == nil || !api.hlsSigner.Verify(id, query.Get("exp"), query.Get("sig")) {
		api.writeError(response, tc.RequestID, http.StatusForbidden, "HLS 签名无效或已过期")
		return
	}
	job, err := api.transcodes.FindTranscodeJob(request.Context(), id)
	if err != nil || job.Status != transcode.StatusReady {
		api.writeError(response, tc.RequestID, http.StatusNotFound, "HLS 尚未就绪")
		return
	}
	prefix := transcode.HLSPrefix(id)
	body, err := api.objectReader.ReadObject(request.Context(), prefix+playlist, 1<<20)
	if err != nil {
		api.writeError(response, tc.RequestID, http.StatusNotFound, "HLS playlist 不存在")
		return
	}
	signedQuery := url.Values{"exp": {query.Get("exp")}, "sig": {query.Get("sig")}}.Encode()
	rewritten, err := rewritePlaylist(body, path.Dir(playlist), signedQuery, func(relative string) (string, error) {
		return api.storage.PresignedGet(request.Context(), prefix+relative, hlsTTL)
	})
	if err != nil {
		api.logger.Warn("改写 HLS playlist 失败", "request_id", tc.RequestID, "recording_id", id, "err", err)
		api.writeError(response, tc.RequestID, http.StatusInternalServerError, "HLS playlist 无效")
		return
	}
	response.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(rewritten)
}

var uriAttribute = regexp.MustCompile(`URI="([^"]*)"`)

// rewritePlaylist 把子 playlist 改为带签名的相对地址（随请求路径解析），把分片与 init 段改为对象存储预签名地址。
// FFmpeg 只产生相对 URI；遇到绝对地址或越出 HLS 目录的路径时报错，而不是透传。
func rewritePlaylist(body []byte, dir, signedQuery string, presign func(string) (string, error)) ([]byte, error) {
	resolve := func(uri string) (string, error) {
		if uri == "" || strings.Contains(uri, "://") || strings.HasPrefix(uri, "/") {
			return "", fmt.Errorf("非相对 URI：%q", uri)
		}
		joined := path.Clean(path.Join(dir, uri))
		if joined == ".." || strings.HasPrefix(joined, "../") {
			return "", fmt.Errorf("URI 越出 HLS 目录：%q", uri)
		}
		if strings.HasSuffix(joined, ".m3u8") {
			return uri + "?" + signedQuery, nil
		}
		return presign(joined)
	}

	var out bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "#"):
			var resolveErr error
			line = uriAttribute.ReplaceAllStringFunc(line, func(match string) string {
				resolved, err := resolve(uriAttribute.FindStringSubmatch(match)[1])
				if err != nil {
					resolveErr = err
					return match
				}
				return `URI="` + resolved + `"`
			})
			if resolveErr != nil {
				return nil, resolveErr
			}
		default:
			resolved, err := resolve(line)
			if err != nil {
				return nil, err
			}
			line = resolved
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// playbackHLSFields 返回回放接口中与转码相关的字段；未启用 HLS 时返回 nil。
func (api *RecordingAPI) playbackHLSFields(ctx context.Context, id uuid.UUID) (map[string]string, error) {
	if api.transcodes == nil {
		return nil, nil
	}
	job, err := api.transcodes.FindTranscodeJob(ctx, id)
	if errors.Is(err, transcode.ErrJobNotFound) {
		return map[string]string{"transcode_status": "none"}, nil
	}
	if err != nil {
		return nil, err
	}
	fields := map[string]string{"transcode_status": string(job.Status)}
	if job.Status == transcode.StatusReady && job.Variants != nil {
		query, _ := api.hlsSigner.Query(id)
		fields["hls_url"] = "/v1/hls/" + id.String() + "/master.m3u8?" + query
		fields["hls_variants_kbps"] = *job.Variants
	}
	if job.Status == transcode.StatusFailed && job.ErrorMessage != nil {
		fields["transcode_error"] = *job.ErrorMessage
	}
	return fields, nil
}
