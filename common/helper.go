package common

import (
	"net"

	"github.com/coalalib/coalago/metrics"
	"github.com/coalalib/coalago/network/session"
	"github.com/coalalib/coalago/pools"

	m "github.com/coalalib/coalago/message"
	"github.com/coalalib/coalago/resource"
)

type SenderIface interface {
	Send(*m.CoAPMessage, *net.UDPAddr) (*m.CoAPMessage, error)

	GetResourcesForPathAndMethod(string, m.CoapMethod) []*resource.CoAPResource
	GetAllPools() *pools.AllPools
	GetSessionForAddress(udpAddr *net.UDPAddr) *session.SecuredSession
	SetSessionForAddress(securedSession *session.SecuredSession, udpAddr *net.UDPAddr)
	GetResourcesForPath(path string) []*resource.CoAPResource
	IsProxyMode() bool

	GetMetrics() *metrics.MetricsList
}
