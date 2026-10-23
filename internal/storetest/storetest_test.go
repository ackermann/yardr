package storetest

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
)

// proxyEnviron are the proxy and CA variables of a box behind a company's
// egress proxy, each spelled out: a list taken from the code would pass
// whatever the code says.
var proxyEnviron = []string{"HTTPS_PROXY=http://proxy.corp:3128", "https_proxy=http://proxy.corp:3128",
	"HTTP_PROXY=http://proxy.corp:3128", "http_proxy=http://proxy.corp:3128",
	"NO_PROXY=localhost,.corp", "no_proxy=localhost,.corp",
	"SSL_CERT_FILE=/etc/corp/ca.pem", "SSL_CERT_DIR=/etc/corp/certs", "NODE_EXTRA_CA_CERTS=/etc/corp/ca.pem"}

// A test binary that imports this package starts with none of the box's
// proxy and CA variables, whatever the box has: the packs' scripts the tests
// run call stand-ins on 127.0.0.1, which curl sends to a proxy it is told of.
// An init cannot be seen from inside the run it began, so the binary runs
// once more with all nine set and says what its environment held when its
// tests began.
func TestInitDropsTheProxy(t *testing.T) {
	const child = "YARDR_STORETEST_PROXY_CHILD"
	if os.Getenv(child) == "1" {
		for _, kv := range proxyEnviron {
			if k, _, _ := strings.Cut(kv, "="); os.Getenv(k) != "" {
				t.Errorf("%s is set in a test binary that imports storetest", k)
			}
		}
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestInitDropsTheProxy$", "-test.v")
	cmd.Env = append(append(os.Environ(), proxyEnviron...), child+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestInitDropsTheProxy") {
		t.Fatalf("the test binary, run with the box's proxy set: %v\n%s", err, out)
	}
}

// NameTestBinary says what the server and the daemon read of it: this
// process as the owner, running, and a tick they take. They take a malformed
// owner for a warning and no more, so a change to either side of the format
// would leave every server of the binary ownerless with no test red but this.
func TestNameTestBinary(t *testing.T) {
	// Serial: sets the process's environment, and puts it back.
	for _, name := range []string{config.TestOwnerEnv, config.TestHomeCheckEnv} {
		old, had := os.LookupEnv(config.Key(name))
		t.Cleanup(func() {
			if had {
				os.Setenv(config.Key(name), old)
			} else {
				os.Unsetenv(config.Key(name))
			}
		})
	}
	NameTestBinary()
	pid, start, ok := config.TestOwner()
	if !ok || pid != os.Getpid() || !peer.Alive(pid, start) {
		t.Errorf("TestOwner() = %d, %d, %v after NameTestBinary: want this process (pid %d), running", pid, start, ok, os.Getpid())
	}
	if every, ok := config.TestHomeCheck(); !ok || every != time.Second {
		t.Errorf("TestHomeCheck() = %v, %v after NameTestBinary: want 1s", every, ok)
	}
}
