package coalago

import "testing"

// Regression test for the open-relay / SSRF fix: sendMultyProxy must refuse to
// forward a Proxy-URI message unless proxy mode was explicitly enabled via
// Server.Proxy(true). Before the fix `proxyEnable` was never checked and any
// peer could use the server as an SSRF pivot (server.go forwarding paths).
func TestSendMultyProxy_RefusesWhenProxyDisabled(t *testing.T) {
	s := &Server{} // proxyEnable defaults to false

	err := s.sendMultyProxy(nil, "127.0.0.1:9999")
	if err == nil {
		t.Fatal("OPEN-RELAY REGRESSION: sendMultyProxy forwarded while proxy disabled")
	}
	if err.Error() != "proxy disabled" {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Log("proxy-disabled path correctly refuses to forward (open-relay fix active)")
}
