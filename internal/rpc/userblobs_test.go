package rpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// blobYard is a mail yard whose listener's log is kept: jo the operator
// and sam, a device each.
func blobYard(t *testing.T) (*store.Store, http.Handler, *bytes.Buffer, string, string) {
	t.Helper()
	st, _, jo, sam := mailYard(t)
	var log bytes.Buffer
	u := NewUserServer(st, func(context.Context) string { return "south" },
		slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return st, u.Handler(), &log, jo, sam
}

// A blob the disk would not take fails on the yard's own file: a device
// that is no operator's is told that it failed and not on which file, and
// the full words are logged for the operator. A device of the operator is
// told the error, which names the file. A file over the yard's cap is a
// refusal, and reads whole to either device.
func TestUserPutBlobHeldWhenTheDiskFails(t *testing.T) {
	ctx := context.Background()

	t.Run("the write fails", func(t *testing.T) {
		st, h, log, jo, sam := blobYard(t)
		// A file where the blobs directory belongs: nothing can be kept.
		blobs := filepath.Dir(st.BlobPath(strings.Repeat("0", 64)))
		if err := os.RemoveAll(blobs); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(blobs, []byte("in the way"), 0o644); err != nil {
			t.Fatal(err)
		}
		put := `{"data":"` + base64.StdEncoding.EncodeToString([]byte("the shed from the phone")) + `"}`

		var held struct {
			Error *wireError `json:"error"`
		}
		raw := call(t, h, sam, "PutBlob", put)
		if err := json.Unmarshal([]byte(raw), &held); err != nil || held.Error == nil {
			t.Fatalf("sam's device is answered %s (%v), want an error", raw, err)
		}
		if held.Error.Message != callHeld {
			t.Errorf("sam's device is told %q, want %q", held.Error.Message, callHeld)
		}
		if strings.Contains(held.Error.Message, blobs) {
			t.Errorf("sam's device is told the file: %q", held.Error.Message)
		}
		if whole := call(t, h, jo, "PutBlob", put); !strings.Contains(whole, blobs) {
			t.Errorf("the operator's device is answered %s, want the error that names the file", whole)
		}
		if !strings.Contains(log.String(), "a device's request failed on the yard's file") ||
			!strings.Contains(log.String(), blobs) {
			t.Errorf("the listener's log does not hold the full words:\n%s", log.String())
		}
	})

	t.Run("a file over the cap is refused whole", func(t *testing.T) {
		st, h, _, jo, sam := blobYard(t)
		if err := st.SetSetting(ctx, store.BlobMaxSetting, "100", "jo"); err != nil {
			t.Fatal(err)
		}
		data := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 200)))
		want := "the file is 200 bytes, over the yard's limit of 100 (yardr settings set blob_max_bytes <bytes>)"
		for name, token := range map[string]string{"sam's device": sam, "the operator's device": jo} {
			var got struct {
				Error *wireError `json:"error"`
			}
			raw := call(t, h, token, "PutBlob", `{"data":"`+data+`"}`)
			if err := json.Unmarshal([]byte(raw), &got); err != nil || got.Error == nil {
				t.Fatalf("%s is answered %s (%v), want a refusal", name, raw, err)
			}
			if got.Error.Message != want {
				t.Errorf("%s is told %q, want the refusal whole %q", name, got.Error.Message, want)
			}
		}
	})
}

// A file the yard holds but cannot read is told without the file: a
// device that is no operator's gets a 500 that says it failed and not on
// which file, and the full words are logged for the operator. A device of
// the operator gets the error, which names the file.
func TestUserBlobReadHeldWhenTheFileCannotBeRead(t *testing.T) {
	ctx := context.Background()
	st, h, log, jo, sam := blobYard(t)
	ref, err := st.PutBlob(ctx, []byte("\x89PNG\r\n\x1a\n the shed on its note"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddNote(ctx, "yd-shed", "hung the picture", "jo",
		store.BlobFile{Hash: ref.Hash, Name: "shed.png"}); err != nil {
		t.Fatal(err)
	}
	// A directory where the blob file was: the yard names it, but cannot
	// read it.
	path := st.BlobPath(ref.Hash)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	w := ask(h, http.MethodGet, "/user/blobs/"+ref.Hash, sam, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("sam's device gets %d, want 500", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != callHeld {
		t.Errorf("sam's device is told %q, want %q", body, callHeld)
	}
	if strings.Contains(w.Body.String(), path) {
		t.Errorf("sam's device is told the file: %q", w.Body.String())
	}
	w = ask(h, http.MethodGet, "/user/blobs/"+ref.Hash, jo, nil)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), path) {
		t.Errorf("the operator's device gets %d %q, want the 500 that names the file", w.Code, w.Body.String())
	}
	if !strings.Contains(log.String(), "a file a device asked for could not be read") ||
		!strings.Contains(log.String(), path) {
		t.Errorf("the listener's log does not hold the full words:\n%s", log.String())
	}

	t.Run("an unclaimed device upload", func(t *testing.T) {
		st, h, log, _, sam := blobYard(t)
		data := base64.StdEncoding.EncodeToString([]byte("a picture not yet in a note"))
		raw := call(t, h, sam, "PutBlob", `{"data":"`+data+`"}`)
		var uploaded struct {
			Result struct {
				Hash string `json:"hash"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(raw), &uploaded); err != nil || uploaded.Result.Hash == "" {
			t.Fatalf("sam's upload is answered %s (%v), want a blob hash", raw, err)
		}
		path := st.BlobPath(uploaded.Result.Hash)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}

		w := ask(h, http.MethodGet, "/user/blobs/"+uploaded.Result.Hash, sam, nil)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("sam's device gets %d, want 500: %q", w.Code, w.Body.String())
		}
		if body := strings.TrimSpace(w.Body.String()); body != callHeld {
			t.Errorf("sam's device is told %q, want %q", body, callHeld)
		}
		if strings.Contains(w.Body.String(), path) {
			t.Errorf("sam's device is told the file: %q", w.Body.String())
		}
		if !strings.Contains(log.String(), "a device's request failed on the yard's file") ||
			!strings.Contains(log.String(), path) {
			t.Errorf("the listener's log does not hold the full words:\n%s", log.String())
		}
	})
}
