package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
	"github.com/jiankailei/jkmeeting/server/internal/transcode"
)

type fakeTranscodes struct{ job *transcode.Job }

func (store fakeTranscodes) FindTranscodeJob(context.Context, uuid.UUID) (transcode.Job, error) {
	if store.job == nil {
		return transcode.Job{}, transcode.ErrJobNotFound
	}
	return *store.job, nil
}

type fakeObjectReader struct{ objects map[string]string }

func (reader fakeObjectReader) ReadObject(_ context.Context, key string, _ int64) ([]byte, error) {
	body, ok := reader.objects[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return []byte(body), nil
}

const masterPlaylist = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-STREAM-INF:BANDWIDTH=36214,AVERAGE-BANDWIDTH=34497,CODECS="mp4a.40.2"
32k/index.m3u8

#EXT-X-STREAM-INF:BANDWIDTH=67933,AVERAGE-BANDWIDTH=66210,CODECS="mp4a.40.2"
64k/index.m3u8
`

const mediaPlaylist = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:6
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-MAP:URI="init_0.mp4"
#EXTINF:6.016000,
seg_000.m4s
#EXT-X-ENDLIST
`

func hlsAPI(t *testing.T, job *transcode.Job) (*RecordingAPI, uuid.UUID, *HLSSigner) {
	t.Helper()
	id := uuid.New()
	prefix := transcode.HLSPrefix(id)
	store := &fakeRecordingStore{recording: recording.Recording{ID: id, ObjectKey: "recordings/" + id.String() + "/audio.ogg"}}
	signer := NewHLSSigner("fixture-token")
	api := NewRecordingAPI(store, &fakeStorage{exists: true}, &fakeDispatcher{}, "fixture-token").
		WithHLS(fakeTranscodes{job: job}, fakeObjectReader{objects: map[string]string{
			prefix + "master.m3u8":    masterPlaylist,
			prefix + "32k/index.m3u8": mediaPlaylist,
		}}, signer)
	return api, id, signer
}

func readyJob() *transcode.Job {
	variants := "32,64"
	return &transcode.Job{Status: transcode.StatusReady, Variants: &variants}
}

func TestPlaybackReportsTranscodeStatusAndSignedHLSURL(t *testing.T) {
	cases := map[string]struct {
		job        *transcode.Job
		wantStatus string
		wantHLS    bool
	}{
		"legacy recording": {nil, "none", false},
		"pending":          {&transcode.Job{Status: transcode.StatusPending}, "pending", false},
		"ready":            {readyJob(), "ready", true},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			api, id, _ := hlsAPI(t, testCase.job)
			request := httptest.NewRequest(http.MethodGet, "/v1/recordings/"+id.String()+"/playback", nil)
			request.Header.Set("Authorization", "Bearer fixture-token")
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)

			var body map[string]string
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil || response.Code != http.StatusOK {
				t.Fatalf("status = %d err = %v", response.Code, err)
			}
			if body["playback_url"] == "" || body["transcode_status"] != testCase.wantStatus {
				t.Fatalf("body = %v", body)
			}
			if hls := body["hls_url"]; (hls != "") != testCase.wantHLS ||
				(testCase.wantHLS && (!strings.HasPrefix(hls, "/v1/hls/"+id.String()+"/master.m3u8?") || body["hls_variants_kbps"] != "32,64")) {
				t.Fatalf("hls fields = %v", body)
			}
		})
	}
}

func TestServeHLSRewritesPlaylistsWithSignedAndPresignedURIs(t *testing.T) {
	api, id, signer := hlsAPI(t, readyJob())
	query, _ := signer.Query(id)

	master := httptest.NewRecorder()
	api.ServeHTTP(master, httptest.NewRequest(http.MethodGet, "/v1/hls/"+id.String()+"/master.m3u8?"+query, nil))
	if master.Code != http.StatusOK || master.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Fatalf("master status = %d headers = %v body = %s", master.Code, master.Header(), master.Body)
	}
	if !strings.Contains(master.Body.String(), "\n32k/index.m3u8?"+query+"\n") || strings.Contains(master.Body.String(), "\n64k/index.m3u8\n") {
		t.Fatalf("master not rewritten with signature:\n%s", master.Body)
	}

	media := httptest.NewRecorder()
	api.ServeHTTP(media, httptest.NewRequest(http.MethodGet, "/v1/hls/"+id.String()+"/32k/index.m3u8?"+query, nil))
	prefix := "http://127.0.0.1:9000/recordings/" + transcode.HLSPrefix(id)
	if media.Code != http.StatusOK || !strings.Contains(media.Body.String(), `#EXT-X-MAP:URI="`+prefix+`32k/init_0.mp4"`) ||
		!strings.Contains(media.Body.String(), "\n"+prefix+"32k/seg_000.m4s\n") {
		t.Fatalf("media status = %d body:\n%s", media.Code, media.Body)
	}
}

func TestServeHLSRejectsBadSignaturesAndPaths(t *testing.T) {
	api, id, signer := hlsAPI(t, readyJob())
	query, _ := signer.Query(id)
	expired := NewHLSSigner("fixture-token")
	expired.now = func() time.Time { return time.Now().Add(-time.Hour) }
	expiredQuery, _ := expired.Query(id)
	otherQuery, _ := signer.Query(uuid.New())
	tampered, _ := url.ParseQuery(query)
	tampered.Set("exp", tampered.Get("exp")+"0")

	cases := map[string]struct {
		path string
		want int
	}{
		"expired":           {"/v1/hls/" + id.String() + "/master.m3u8?" + expiredQuery, http.StatusForbidden},
		"other recording":   {"/v1/hls/" + id.String() + "/master.m3u8?" + otherQuery, http.StatusForbidden},
		"tampered exp":      {"/v1/hls/" + id.String() + "/master.m3u8?" + tampered.Encode(), http.StatusForbidden},
		"missing signature": {"/v1/hls/" + id.String() + "/master.m3u8", http.StatusForbidden},
		"segment path":      {"/v1/hls/" + id.String() + "/32k/seg_000.m4s?" + query, http.StatusBadRequest},
		"traversal":         {"/v1/hls/" + id.String() + "/../audio.ogg?" + query, http.StatusBadRequest},
		"bad id":            {"/v1/hls/not-a-uuid/master.m3u8?" + query, http.StatusBadRequest},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.URL, _ = url.Parse(testCase.path)
			api.ServeHTTP(response, request)
			if response.Code != testCase.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, testCase.want, response.Body)
			}
		})
	}
}

func TestRewritePlaylistRejectsAbsoluteAndEscapingURIs(t *testing.T) {
	presign := func(key string) (string, error) { return "https://s3/" + key, nil }
	for _, body := range []string{
		"#EXTM3U\nhttp://evil.example/seg.m4s\n",
		"#EXTM3U\n/etc/passwd\n",
		"#EXTM3U\n../../audio.ogg\n",
		"#EXTM3U\n#EXT-X-MAP:URI=\"../../init.mp4\"\n",
	} {
		if _, err := rewritePlaylist([]byte(body), "32k", "exp=1&sig=x", presign); err == nil {
			t.Fatalf("rewritePlaylist(%q) error = nil, want error", body)
		}
	}
}
