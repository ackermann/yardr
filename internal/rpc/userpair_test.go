package rpc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// pairYard is a yard with the operator jo, the user sam and no device, on a
// listener of its own at 127.0.0.1:0, and a clock for its device pairings
// that the test moves.
type pairYard struct {
	st   *store.Store
	u    *UserServer
	base string
	late atomic.Int64 // how far the clock is ahead, in nanoseconds
}

func newPairYard(t *testing.T) *pairYard {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(context.Background()) })
	for _, name := range []string{"jo", "sam"} {
		if _, err := st.AddUser(ctx, name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	y := &pairYard{st: st, u: NewUserServer(st, func(context.Context) string { return "south" }, nil)}
	st.SetDevicePairClock(func() time.Time { return time.Now().Add(time.Duration(y.late.Load())) })
	y.base = listen(t, y.u, 5*time.Second)
	return y
}

// open opens a pairing for the device of the user and answers its code.
func (y *pairYard) open(t *testing.T, user, device string) string {
	t.Helper()
	p, err := y.st.DevicePairOpen(context.Background(), user, device, "jo")
	if err != nil {
		t.Fatal(err)
	}
	return p.Code
}

// post sends body to path over the yard's listener and answers the status
// and the body, with the token when there is one.
func (y *pairYard) post(t *testing.T, path, token, body string) (int, string) {
	t.Helper()
	return y.do(t, http.MethodPost, path, token, nil, body)
}

// do is post for any method, with the headers of header besides.
func (y *pairYard) do(t *testing.T, method, path, token string, header http.Header, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, y.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range header {
		req.Header[name] = values
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(answer))
}

func (y *pairYard) redeem(t *testing.T, code string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code})
	return y.post(t, PairPath, "", string(body))
}

// redeemAs is redeem with the edge's word for who presents it: one
// PresenterHeader line for each of presenters, so none is redeem.
func (y *pairYard) redeemAs(t *testing.T, code string, presenters ...string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code})
	return y.do(t, http.MethodPost, PairPath, "", http.Header{PresenterHeader: presenters}, string(body))
}

// wrong is a code that is one, and not the code.
func wrong(code string) string {
	if strings.HasPrefix(code, "0") {
		return "1" + code[1:]
	}
	return "0" + code[1:]
}

// A device that calls with the code of the open pairing is answered with
// its token, once: the token then calls as the user the pairing was opened
// for, the store has its hash and never the token, and the device is as one
// added by hand, with the event saying which pairing it came by. The yard
// had no device before, so nothing else was under /user/.
func TestDevicePairedByItsCode(t *testing.T) {
	y := newPairYard(t)
	ctx := context.Background()
	if code, _ := y.post(t, PairPath, "", `{"code":"K7QM-2XPD"}`); code != http.StatusNotFound {
		t.Fatalf("POST %s with no pairing open, in a yard with no device = %d, want 404", PairPath, code)
	}
	code := y.open(t, "sam", "phone")
	// As it is typed: without the dash, in small letters.
	status, body := y.redeem(t, strings.ToLower(strings.ReplaceAll(code, "-", "")))
	var got Paired
	if err := json.Unmarshal([]byte(body), &got); status != http.StatusOK || err != nil {
		t.Fatalf("the code = %d %s (%v)", status, body, err)
	}
	if !strings.HasPrefix(got.Token, store.DeviceTokenPrefix) || got.User != "sam" || got.Device != "phone" || got.Yard != "south" {
		t.Fatalf("the code is answered %+v", got)
	}
	if status, me := y.get(t, "/user/me", got.Token); status != http.StatusOK || me != `{"user":"sam","said_by":"phone","yard":"south"}` {
		t.Errorf("/user/me with the token = %d %s", status, me)
	}
	devices, err := y.st.Devices(ctx, "")
	if err != nil || len(devices) != 1 || devices[0].User != "sam" || devices[0].Name != "phone" || devices[0].CreatedBy != "jo" {
		t.Fatalf("devices = %+v, %v", devices, err)
	}
	added := eventsOfKind(t, y.st, "device_added")
	if len(added) != 1 || added[0].Actor != "jo" || added[0].Data["user"] != "sam" || added[0].Data["device"] != "phone" || added[0].Data["pairing"] == "" {
		t.Fatalf("device_added = %+v", added)
	}
	opened := eventsOfKind(t, y.st, "device_pair_opened")
	if len(opened) != 1 || opened[0].Data["pairing"] != added[0].Data["pairing"] {
		t.Fatalf("device_pair_opened = %+v", opened)
	}
	// Neither the code nor the token is in any event.
	all, err := y.st.Events(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	said, _ := json.Marshal(all)
	for _, secret := range []string{code, strings.ReplaceAll(code, "-", ""), got.Token} {
		if strings.Contains(string(said), secret) {
			t.Errorf("the events have %q", secret)
		}
	}
}

func (y *pairYard) get(t *testing.T, path, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, y.base+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(answer))
}

// A code is good once: the second call with it finds no pairing, and so no
// path. The yard has a device by then, so the path is answered as every
// path under /user/ is without a credential, and as one that is not served
// with one. No second device is added.
func TestDevicePairCodeIsGoodOnce(t *testing.T) {
	y := newPairYard(t)
	code := y.open(t, "jo", "phone")
	status, body := y.redeem(t, code)
	var got Paired
	if err := json.Unmarshal([]byte(body), &got); status != http.StatusOK || err != nil {
		t.Fatalf("the code = %d %s (%v)", status, body, err)
	}
	if status, body := y.redeem(t, code); status != http.StatusUnauthorized || strings.Contains(body, "token") {
		t.Errorf("the code again = %d %s, want 401", status, body)
	}
	if status, body := y.post(t, PairPath, got.Token, `{"code":"`+code+`"}`); status != http.StatusNotFound || strings.Contains(body, "yardr-dev-") {
		t.Errorf("the code again, as the device = %d %s, want 404", status, body)
	}
	if devices, err := y.st.Devices(context.Background(), ""); err != nil || len(devices) != 1 {
		t.Fatalf("devices = %+v, %v", devices, err)
	}
	if got := eventsOfKind(t, y.st, "device_pair_refused"); len(got) != 0 {
		t.Errorf("a pairing that was used is logged as refused: %+v", got)
	}
}

// A wrong code is refused and counted, and the fifth ends the pairing: the
// right code is good for nothing then. What is no code at all is no try.
func TestDevicePairWrongCodeFiveTimes(t *testing.T) {
	y := newPairYard(t)
	code := y.open(t, "jo", "phone")
	for _, body := range []string{``, `[]`, `{"code":"K7QM"}`, `{"code":"K7QM-2XPD-9"}`, `{"code":17}`, `{}`} {
		if status, answer := y.post(t, PairPath, "", body); status != http.StatusBadRequest {
			t.Errorf("POST %s %s = %d %s, want 400", PairPath, body, status, answer)
		}
	}
	for try := 1; try <= store.MaxTries; try++ {
		if status, body := y.redeem(t, wrong(code)); status != http.StatusForbidden || body != `{"refused":"wrong_code"}` {
			t.Fatalf("wrong code %d = %d %s, want 403 wrong_code", try, status, body)
		}
	}
	// No device in the yard, and no pairing: nothing is under /user/.
	if status, body := y.redeem(t, code); status != http.StatusNotFound || strings.Contains(body, "yardr-dev-") {
		t.Errorf("the right code after %d wrong ones = %d %s, want 404", store.MaxTries, status, body)
	}
	if devices, err := y.st.Devices(context.Background(), ""); err != nil || len(devices) != 0 {
		t.Fatalf("devices = %+v, %v", devices, err)
	}
	ended := eventsOfKind(t, y.st, "device_pair_refused")
	if len(ended) != 1 || ended[0].Data["reason"] != store.WrongCode || ended[0].Data["wrong_codes"] != float64(store.MaxTries) ||
		ended[0].Data["user"] != "jo" || ended[0].Data["device"] != "phone" {
		t.Fatalf("device_pair_refused = %+v", ended)
	}

	// Four wrong ones leave the code good, and another pairing may be opened
	// once this one has ended.
	code = y.open(t, "jo", "phone")
	for try := 1; try < store.MaxTries; try++ {
		y.redeem(t, wrong(code))
	}
	if status, body := y.redeem(t, code); status != http.StatusOK {
		t.Errorf("the right code after %d wrong ones = %d %s", store.MaxTries-1, status, body)
	}
}

// A redemption that names who presents it (PresenterHeader), as the edge
// names the signed-in account of one it forwards, is the pairing's user's
// alone: the address is mapped to a user as web_jwt_users has it, whatever
// its case. Any other presenter is refused as a wrong code is, with the same
// answer and the same count, and the code is neither looked at nor used, so
// the user it was opened for can still redeem it, but for an address that
// maps to nobody where the pairing's user maps to nobody too, which redeems
// as with no presenter. The code is still required of the right presenter.
// The header is read on this call alone.
func TestDevicePairPresenter(t *testing.T) {
	y := newPairYard(t)
	ctx := context.Background()
	if err := y.st.SetSetting(ctx, store.WebJWTUsersSetting, "jo@example.net=jo, Sam@Example.NET=sam", "jo"); err != nil {
		t.Fatal(err)
	}
	refused := func(what string, status int, body string) {
		t.Helper()
		if status != http.StatusForbidden || body != `{"refused":"wrong_code"}` {
			t.Errorf("%s = %d %s, want 403 wrong_code", what, status, body)
		}
	}
	devices := func(want int) {
		t.Helper()
		if got, err := y.st.Devices(ctx, ""); err != nil || len(got) != want {
			t.Fatalf("devices = %+v, %v, want %d", got, err, want)
		}
	}

	// The right code, presented by another user of the yard and by an
	// address nobody is mapped to, is refused and leaves the code good.
	code := y.open(t, "sam", "phone")
	status, body := y.redeemAs(t, code, "jo@example.net")
	refused("sam's code presented by jo", status, body)
	status, body = y.redeemAs(t, code, "eve@example.net")
	refused("sam's code presented by an address no user is mapped to", status, body)
	devices(0)

	status, body = y.redeemAs(t, code, "SAM@example.net")
	var got Paired
	if err := json.Unmarshal([]byte(body), &got); status != http.StatusOK || err != nil || got.User != "sam" || got.Device != "phone" {
		t.Fatalf("sam's code presented by sam = %d %s (%v)", status, body, err)
	}
	devices(1)

	// They count as wrong codes do, the fifth ending the pairing whichever
	// they are, and a header that is no address names nobody: sent empty,
	// or twice.
	code = y.open(t, "sam", "laptop")
	for _, try := range []struct {
		what       string
		code       string
		presenters []string
	}{
		{"jo", code, []string{"jo@example.net"}},
		{"an address no user is mapped to", code, []string{"eve@example.net"}},
		{"a header with no address", code, []string{""}},
		{"a header twice, sam both times", code, []string{"sam@example.net", "sam@example.net"}},
		{"sam, and a code that is not the code", wrong(code), []string{"sam@example.net"}},
	} {
		status, body := y.redeemAs(t, try.code, try.presenters...)
		refused(try.what, status, body)
	}
	if status, body := y.redeemAs(t, code, "sam@example.net"); status != http.StatusUnauthorized || strings.Contains(body, "yardr-dev-") {
		t.Errorf("sam's code after %d refusals = %d %s, want 401", store.MaxTries, status, body)
	}
	devices(1)
	ended := eventsOfKind(t, y.st, "device_pair_refused")
	if len(ended) != 1 || ended[0].Data["reason"] != store.WrongCode || ended[0].Data["wrong_codes"] != float64(store.MaxTries) ||
		ended[0].Data["user"] != "sam" || ended[0].Data["device"] != "laptop" {
		t.Fatalf("device_pair_refused = %+v", ended)
	}

	// No other call reads it: the device just paired calls as sam, whoever
	// the header says presents the call.
	if status, me := y.do(t, http.MethodGet, "/user/me", got.Token, http.Header{PresenterHeader: {"jo@example.net"}}, ""); status != http.StatusOK ||
		me != `{"user":"sam","said_by":"phone","yard":"south"}` {
		t.Errorf("/user/me naming jo as the presenter = %d %s", status, me)
	}

	// Where the presenter maps to nobody, a pairing whose user the mapping
	// names stays bound to its opener, while a pairing for an unmapped user
	// redeems as with no presenter.
	if err := y.st.SetSetting(ctx, store.WebJWTUsersSetting, "sam@example.net=sam", "jo"); err != nil {
		t.Fatal(err)
	}
	code = y.open(t, "sam", "tablet")
	status, body = y.redeemAs(t, code, "eve@example.net")
	refused("sam's code presented by an address no user is mapped to, sam mapped", status, body)
	status, body = y.redeemAs(t, code, "sam@example.net")
	if err := json.Unmarshal([]byte(body), &got); status != http.StatusOK || err != nil || got.User != "sam" || got.Device != "tablet" {
		t.Fatalf("sam's code presented by sam = %d %s (%v)", status, body, err)
	}
	devices(2)
	code = y.open(t, "jo", "tablet")
	status, body = y.redeemAs(t, code, "jo@example.net")
	if err := json.Unmarshal([]byte(body), &got); status != http.StatusOK || err != nil || got.User != "jo" || got.Device != "tablet" {
		t.Fatalf("jo's code presented by an unmapped address, jo unmapped = %d %s (%v)", status, body, err)
	}
	devices(3)

	// With no mapping at all every address names nobody, and the code
	// redeems for an unmapped user; with no header the code alone redeems,
	// as it did.
	if err := y.st.SetSetting(ctx, store.WebJWTUsersSetting, "", "jo"); err != nil {
		t.Fatal(err)
	}
	code = y.open(t, "jo", "watch")
	status, body = y.redeemAs(t, code, "owner@example.com")
	if err := json.Unmarshal([]byte(body), &got); status != http.StatusOK || err != nil || got.User != "jo" || got.Device != "watch" {
		t.Fatalf("jo's code presented by an address when web_jwt_users is unset = %d %s (%v)", status, body, err)
	}
	devices(4)
	code = y.open(t, "jo", "buds")
	status, body = y.redeem(t, code)
	if err := json.Unmarshal([]byte(body), &got); status != http.StatusOK || err != nil || got.User != "jo" || got.Device != "buds" {
		t.Fatalf("jo's code with no presenter named = %d %s (%v)", status, body, err)
	}
	devices(5)

	// Accepted: a teammate whose line was removed while the edge still
	// holds them active redeems the owner's pairing with its code, since
	// neither maps to a user then. The code is still required.
	if err := y.st.SetSetting(ctx, store.WebJWTUsersSetting, "sam@example.net=sam", "jo"); err != nil {
		t.Fatal(err)
	}
	code = y.open(t, "jo", "earbuds")
	if err := y.st.SetSetting(ctx, store.WebJWTUsersSetting, "", "jo"); err != nil {
		t.Fatal(err)
	}
	status, body = y.redeemAs(t, code, "sam@example.net")
	if err := json.Unmarshal([]byte(body), &got); status != http.StatusOK || err != nil || got.User != "jo" || got.Device != "earbuds" {
		t.Fatalf("jo's code presented by a removed teammate's address = %d %s (%v)", status, body, err)
	}
	devices(6)
}

// A code runs out after store.TTL: it is good a second before, and after
// it the path is gone and no device is added.
func TestDevicePairCodeRunsOut(t *testing.T) {
	y := newPairYard(t)
	code := y.open(t, "jo", "phone")
	y.late.Store(int64(store.TTL + time.Second))
	if status, body := y.redeem(t, code); status != http.StatusNotFound || strings.Contains(body, "yardr-dev-") {
		t.Errorf("the code after %s = %d %s, want 404", store.TTL, status, body)
	}
	if devices, err := y.st.Devices(context.Background(), ""); err != nil || len(devices) != 0 {
		t.Fatalf("devices = %+v, %v", devices, err)
	}
	if ended := eventsOfKind(t, y.st, "device_pair_refused"); len(ended) != 1 || ended[0].Data["reason"] != store.TimedOut {
		t.Fatalf("device_pair_refused = %+v", ended)
	}
	code = y.open(t, "jo", "phone")
	y.late.Add(int64(store.TTL - time.Second))
	if status, body := y.redeem(t, code); status != http.StatusOK {
		t.Errorf("the code a second before it runs out = %d %s", status, body)
	}
}

// The path takes a pairing's code and nothing else, and only as a POST:
// with a pairing open, every other path under /user/ is as it was, and the
// body of a request that is refused there is still not read.
func TestDevicePairPathIsTheOnlyOneOpened(t *testing.T) {
	st, u, token := userYard(t)
	h := u.Handler()
	if _, err := st.DevicePairOpen(context.Background(), "sam", "laptop", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, p := range append([][2]string{{http.MethodGet, PairPath}, {http.MethodPut, PairPath}, {http.MethodPost, PairPath + "/"}, {http.MethodPost, PairPath + "/more"}}, userPaths...) {
		if w := ask(h, p[0], p[1], "", unread{t}); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a credential, a pairing open = %d, want 401", p[0], p[1], w.Code)
		}
	}
	if w := ask(h, http.MethodGet, "/user/me", token, nil); w.Code != http.StatusOK {
		t.Errorf("/user/me as a device, a pairing open = %d", w.Code)
	}
	// Ended by the operator, the path is as the others.
	if _, err := st.DevicePairClose(context.Background(), "jo"); err != nil {
		t.Fatal(err)
	}
	if w := ask(h, http.MethodPost, PairPath, "", unread{t}); w.Code != http.StatusUnauthorized {
		t.Errorf("POST %s after the pairing was ended = %d, want 401", PairPath, w.Code)
	}
}
