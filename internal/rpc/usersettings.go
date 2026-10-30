package rpc

import (
	"context"
	"strconv"

	"yardr.dev/yardr/internal/store"
)

// routerAddr is the yard's router_addr setting: the address of its router
// on the user's network, "" when unset. It is read on every call, so a
// change moves nothing that runs; what cannot be read is no router.
func (u *UserServer) routerAddr(ctx context.Context) string {
	all, err := u.st.Settings(ctx)
	if err != nil {
		return ""
	}
	return all[store.RouterAddrSetting]
}

// hosted reports whether the yard is one the service hosts: its hosted
// setting, which the provisioner writes. It is read on every call, as
// router_addr is, so a change moves nothing that runs; what cannot be read is
// not hosted, so that a failure to look refuses a secret and never takes one.
func (u *UserServer) hosted(ctx context.Context) bool {
	all, err := u.st.Settings(ctx)
	if err != nil {
		return false
	}
	return all[store.HostedSetting] == "true"
}

// yardmaster is the name the yard's coordinator carries: the yardmaster
// setting, its default where unset. It is read on every call, as
// router_addr is, so a rename moves the next answer with it; what cannot
// be read is the default, as the path answered before the setting was
// told.
func (u *UserServer) yardmaster(ctx context.Context) string {
	if all, err := u.st.Settings(ctx); err == nil && all[store.YardmasterSetting] != "" {
		return all[store.YardmasterSetting]
	}
	return "yardmaster"
}

// blobMax is the yard's cap on a blob, which PutBlob holds its data to.
func (u *UserServer) blobMax(ctx context.Context) int {
	all, _ := u.st.Settings(ctx)
	if n, err := strconv.Atoi(all[store.BlobMaxSetting]); err == nil && n > 0 {
		return n
	}
	return store.DefaultBlobMax
}
