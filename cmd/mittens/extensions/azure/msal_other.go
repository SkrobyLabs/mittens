//go:build !darwin

package azure

// extractMSALCache is a no-op on non-macOS platforms where tokens are
// already stored in the plaintext msal_token_cache.json file.
func extractMSALCache(staging string, subscriptionIDs []string) {}
