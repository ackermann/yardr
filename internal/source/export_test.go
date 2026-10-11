package source

import "time"

// SetMintClock has minted tokens aged by now, and starts with none in hand;
// the returned function puts the clock back.
func SetMintClock(now func() time.Time) (restore func()) {
	old := mintNow
	mintNow = now
	ForgetMintedTokens()
	return func() { mintNow = old; ForgetMintedTokens() }
}

// ForgetMintedTokens drops the tokens in hand, as a new process starts.
func ForgetMintedTokens() {
	mintedTokens.Lock()
	defer mintedTokens.Unlock()
	clear(mintedTokens.m)
}
