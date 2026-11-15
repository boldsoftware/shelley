package channels

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
)

func transportErrorCategory(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.Timeout() {
			return "timeout"
		}
		return "DNS lookup failed"
	}
	var certErr *x509.UnknownAuthorityError
	var certValue x509.UnknownAuthorityError
	var hostnameErr *x509.HostnameError
	var hostnameValue x509.HostnameError
	var invalidErr *x509.CertificateInvalidError
	var invalidValue x509.CertificateInvalidError
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) || errors.As(err, &certValue) ||
		errors.As(err, &hostnameErr) || errors.As(err, &hostnameValue) ||
		errors.As(err, &invalidErr) || errors.As(err, &invalidValue) ||
		errors.As(err, &verifyErr) {
		return "TLS certificate verification failed"
	}
	var recordErr tls.RecordHeaderError
	var alertErr tls.AlertError
	if errors.As(err, &recordErr) || errors.As(err, &alertErr) {
		return "TLS protocol error"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "connection failed"
	}
	return "transport error"
}
