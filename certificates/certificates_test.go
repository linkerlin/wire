package certificates_test

import (
	"crypto/x509"
	"testing"
	"time"

	"github.com/influx6/faux/tests"
	"github.com/wirekit/wire/certificates"
)

func TestCertificateRequestService(t *testing.T) {
	service := certificates.CertificateAuthorityProfile{
		Local:        "Lagos",
		Organization: "DreamBench",
		CommonName:   "DreamBench Inc",
		Country:      "Nigeria",
		Province:     "South-West",
	}

	service.KeyStrength = 4096
	service.LifeTime = (time.Hour * 8760)
	service.Emails = append([]string{}, "alex.ewetumo@dreambench.io")

	ca, err := certificates.CreateCertificateAuthority(service)
	if err != nil {
		tests.FailedWithError(err, "Should have generated new CertificateAuthority")
	}
	tests.Passed("Should have generated new CertificateAuthority")

	requestService := certificates.CertificateRequestProfile{
		Local:        "Lagos",
		Organization: "DreamBench",
		CommonName:   "DreamBench Inc",
		Country:      "Nigeria",
		Province:     "South-West",
	}
	requestService.KeyStrength = 2048

	reqCA, err := certificates.CreateCertificateRequest(requestService)
	if err != nil {
		tests.FailedWithError(err, "Should have generated new CertificateRequest")
	}
	tests.Passed("Should have generated new CertificateRequest")

	// Generate Client and Server Auth Certificate.
	if err := ca.ApproveServerClientCertificateSigningRequest(&reqCA, time.Hour*8760); err != nil {
		tests.FailedWithError(err, "Should have generated new CertificateRequest")
	}
	tests.Passed("Should have generated new CertificateRequest")

	if reqCA.SecondaryCA.RootCA == nil {
		tests.FailedWithError(err, "Should have generated new Certificate for request")
	}
	tests.Passed("Should have generated new Certificate for request")
}

func TestCertificateRequestServiceForClient(t *testing.T) {
	service := certificates.CertificateAuthorityProfile{
		Local:        "Lagos",
		Organization: "DreamBench",
		CommonName:   "DreamBench Inc",
		Country:      "Nigeria",
		Province:     "South-West",
	}

	service.KeyStrength = 4096
	service.LifeTime = (time.Hour * 8760)
	service.Emails = append([]string{}, "alex.ewetumo@dreambench.io")

	ca, err := certificates.CreateCertificateAuthority(service)
	if err != nil {
		tests.FailedWithError(err, "Should have generated new CertificateAuthority")
	}
	tests.Passed("Should have generated new CertificateAuthority")

	requestService := certificates.CertificateRequestProfile{
		Local:        "Lagos",
		Organization: "DreamBench",
		CommonName:   "DreamBench Inc",
		Country:      "Nigeria",
		Province:     "South-West",
	}
	requestService.KeyStrength = 2048

	reqCA, err := certificates.CreateCertificateRequest(requestService)
	if err != nil {
		tests.FailedWithError(err, "Should have generated new CertificateRequest")
	}
	tests.Passed("Should have generated new CertificateRequest")

	// Generate Client and Server Auth Certificate.
	if err := ca.ApproveClientCertificateSigningRequest(&reqCA, time.Hour*8760); err != nil {
		tests.FailedWithError(err, "Should have generated new CertificateRequest")
	}
	tests.Passed("Should have generated new CertificateRequest")

	if _, err = reqCA.TLSCertPool(); err != nil {
		tests.FailedWithError(err, "Should have successfully created x409.CertPool")
	}
	tests.Passed("Should have successfully created x409.CertPool")

	if _, err = reqCA.TLSClientConfig(); err != nil {
		tests.FailedWithError(err, "Should have successfully created client tls.Config")
	}
	tests.Passed("Should have successfully created client tls.Config")

	if _, err = reqCA.TLSServerConfig(false); err != nil {
		tests.FailedWithError(err, "Should have successfully created root tls.Config")
	}
	tests.Passed("Should have successfully created root tls.Config")
}

func TestCertificateRequestServiceForServer(t *testing.T) {
	service := certificates.CertificateAuthorityProfile{
		Local:        "Lagos",
		Organization: "DreamBench",
		CommonName:   "DreamBench Inc",
		Country:      "Nigeria",
		Province:     "South-West",
	}

	service.KeyStrength = 4096
	service.LifeTime = (time.Hour * 8760)
	service.Emails = append([]string{}, "alex.ewetumo@dreambench.io")

	ca, err := certificates.CreateCertificateAuthority(service)
	if err != nil {
		tests.FailedWithError(err, "Should have generated new CertificateAuthority")
	}
	tests.Passed("Should have generated new CertificateAuthority")

	requestService := certificates.CertificateRequestProfile{
		Local:        "Lagos",
		Organization: "DreamBench",
		CommonName:   "DreamBench Inc",
		Country:      "Nigeria",
		Province:     "South-West",
	}
	requestService.KeyStrength = 2048

	reqCA, err := certificates.CreateCertificateRequest(requestService)
	if err != nil {
		tests.FailedWithError(err, "Should have generated new CertificateRequest")
	}
	tests.Passed("Should have generated new CertificateRequest")

	// Generate Client and Server Auth Certificate.
	if err := ca.ApproveServerCertificateSigningRequest(&reqCA, time.Hour*8760); err != nil {
		tests.FailedWithError(err, "Should have generated new CertificateRequest")
	}
	tests.Passed("Should have generated new CertificateRequest")

	if _, err = reqCA.TLSCertPool(); err != nil {
		tests.FailedWithError(err, "Should have successfully created x409.CertPool")
	}
	tests.Passed("Should have successfully created x409.CertPool")

	if _, err = reqCA.TLSClientConfig(); err != nil {
		tests.FailedWithError(err, "Should have successfully created client tls.Config")
	}
	tests.Passed("Should have successfully created client tls.Config")

	if _, err = reqCA.TLSServerConfig(false); err != nil {
		tests.FailedWithError(err, "Should have successfully created root tls.Config")
	}
	tests.Passed("Should have successfully created root tls.Config")
}

func TestCertificateRequestServiceForClientWithVerify(t *testing.T) {
	service := certificates.CertificateAuthorityProfile{
		Local:        "Lagos",
		Organization: "DreamBench",
		CommonName:   "DreamBench Inc",
		Country:      "Nigeria",
		Province:     "South-West",
	}

	service.KeyStrength = 4096
	service.LifeTime = (time.Hour * 8760)
	service.Emails = append([]string{}, "alex.ewetumo@dreambench.io")

	ca, err := certificates.CreateCertificateAuthority(service)
	if err != nil {
		tests.FailedWithError(err, "Should have generated new CertificateAuthority")
	}
	tests.Passed("Should have generated new CertificateAuthority")

	requestService := certificates.CertificateRequestProfile{
		Local:        "Lagos",
		Organization: "DreamBench",
		CommonName:   "DreamBench Inc",
		Country:      "Nigeria",
		Province:     "South-West",
	}
	requestService.KeyStrength = 2048

	reqCA, err := certificates.CreateCertificateRequest(requestService)
	if err != nil {
		tests.FailedWithError(err, "Should have generated new CertificateRequest")
	}
	tests.Passed("Should have generated new CertificateRequest")

	// Generate Client and Server Auth Certificate.
	if err := ca.ApproveServerClientCertificateSigningRequest(&reqCA, time.Hour*8760); err != nil {
		tests.FailedWithError(err, "Should have generated new CertificateRequest")
	}
	tests.Passed("Should have generated new CertificateRequest")

	if err = ca.VerifyCA(reqCA.SecondaryCA.Certificate, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}); err != nil {
		tests.FailedWithError(err, "Should have verified certificate through CA")
	}
	tests.Passed("Should have verified certificate through CA")

	if _, err = reqCA.TLSCertPool(); err != nil {
		tests.FailedWithError(err, "Should have successfully created x409.CertPool")
	}
	tests.Passed("Should have successfully created x409.CertPool")

	if _, err = reqCA.TLSClientConfig(); err != nil {
		tests.FailedWithError(err, "Should have successfully created client tls.Config")
	}
	tests.Passed("Should have successfully created client tls.Config")

	if _, err = reqCA.TLSServerConfig(false); err != nil {
		tests.FailedWithError(err, "Should have successfully created root tls.Config")
	}
	tests.Passed("Should have successfully created root tls.Config")

}

func TestCreateCACertificate(t *testing.T) {
	service := certificates.CertificateAuthorityProfile{
		Local:        "Lagos",
		Organization: "DreamBench",
		CommonName:   "*.dreambench.io",
		Country:      "Nigeria",
		Province:     "South-West",
	}

	service.KeyStrength = 4096
	service.LifeTime = (time.Hour * 8760)
	service.Emails = append([]string{}, "alex.ewetumo@dreambench.io")

	ca, err := certificates.CreateCertificateAuthority(service)
	if err != nil {
		tests.FailedWithError(err, "Should have generated new CertificateAuthority")
	}
	tests.Passed("Should have generated new CertificateAuthority")

	if !ca.Certificate.IsCA {
		tests.Failed("Certificate should be a CA")
	}
	tests.Passed("Certificate should be a CA")
}
