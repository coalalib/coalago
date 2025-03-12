package coalago

import "sync/atomic"

var (
	MetricReceivedMessages,
	MetricBreakedMessages,
	MetricSentMessages,
	MetricRetransmitMessages,
	MetricExpiredMessages,
	MetricSentMessageErrors,
	MetricSessionsRate,
	MetricSessionsCount,
	MetricSuccessfulHandhshakes,
	MetricMaxMTU counterImpl
)

type Counter interface {
	Inc()
	Dec()
	Set(int64)
}

type counterImpl struct {
	c int64
}

func (c *counterImpl) Inc() {
	atomic.AddInt64(&c.c, 1)
}

func (c *counterImpl) Dec() {
	atomic.AddInt64(&c.c, -1)
}

func (c *counterImpl) Set(d int64) {
	atomic.StoreInt64(&c.c, d)
}

func (c *counterImpl) Val() int64 {
	return atomic.LoadInt64(&c.c)
}
