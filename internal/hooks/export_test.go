package hooks

import "time"

// SetGitHubClock has installation tokens dated and aged by now, and starts
// with none in hand; the returned function puts the clock back.
func SetGitHubClock(now func() time.Time) (restore func()) {
	old := githubNow
	githubNow = now
	ForgetGitHubTokens()
	return func() { githubNow = old; ForgetGitHubTokens() }
}

// ForgetGitHubTokens drops the installation tokens in hand, as a new
// process starts.
func ForgetGitHubTokens() {
	githubTokens.Lock()
	defer githubTokens.Unlock()
	clear(githubTokens.m)
}
