package certificates_test

import (
	"bytes"
	"crypto/x509"
	"testing"
	"time"

	"github.com/influx6/faux/tests"
	"github.com/wirekit/wire/certificates"
	"github.com/wirekit/wire/mocks"
)

func TestCertificateRequestService(t *testing.T) {
	storeMap := make(map[string][]byte)
	var store mocks.PersistenceStoreMock
	store.GetFunc, store.PersistFunc = mocks.MapStore(storeMap)

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
	storeMap := make(map[string][]byte)
	var store mocks.PersistenceStoreMock
	store.GetFunc, store.PersistFunc = mocks.MapStore(storeMap)

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
	storeMap := make(map[string][]byte)
	var store mocks.PersistenceStoreMock
	store.GetFunc, store.PersistFunc = mocks.MapStore(storeMap)

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

func TestCertificateRequestRawLoading(t *testing.T) {
	storeMap := make(map[string][]byte)
	var store mocks.PersistenceStoreMock
	store.GetFunc, store.PersistFunc = mocks.MapStore(storeMap)

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

	raw, err := reqCA.Raw()
	if err != nil {
		tests.FailedWithError(err, "Should have generated raw version of CertificateRequest")
	}
	tests.Passed("Should have generated raw version of CertificateRequest")

	var rca certificates.CertificateRequest
	if err := rca.FromRaw(raw); err != nil {
		tests.FailedWithError(err, "Should have read raw of CertificateRequest")
	}
	tests.Passed("Should have read raw  of CertificateRequest")

}

func TestCertificateService(t *testing.T) {
	storeMap := make(map[string][]byte)
	var store mocks.PersistenceStoreMock
	store.GetFunc, store.PersistFunc = mocks.MapStore(storeMap)

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

	if err := ca.Persist(store); err != nil {
		tests.FailedWithError(err, "Should have successfully store certificate into persistence store")
	}
	tests.Passed("Should have successfully store certificate into persistence store")

	var restoredCA certificates.CertificateAuthority
	if err := restoredCA.Load(store); err != nil {
		tests.FailedWithError(err, "Should have successfully retrieved certificate from store")
	}
	tests.Passed("Should have successfully retrieved certificate from store")

	caRaw, err := ca.CertificateRaw()
	if err != nil {
		tests.FailedWithError(err, "Should have been able to retrieve raw form of certificate")
	}
	tests.Passed("Should have been able to retrieve raw form of certificate")

	rcaRaw, err := restoredCA.CertificateRaw()
	if err != nil {
		tests.FailedWithError(err, "Should have been able to retrieve raw form of certificate")
	}
	tests.Passed("Should have been able to retrieve raw form of certificate")

	if !bytes.Equal(caRaw, rcaRaw) {
		tests.Failed("Should have matching certificate raw data between real and restored versions")
	}
	tests.Passed("Should have matching certificate raw data between real and restored versions")

	caKeyRaw, err := ca.PrivateKeyRaw()
	if err != nil {
		tests.FailedWithError(err, "Should have been able to retrieve raw form of certificate")
	}
	tests.Passed("Should have been able to retrieve raw form of certificate")

	rcaKeyRaw, err := restoredCA.PrivateKeyRaw()
	if err != nil {
		tests.FailedWithError(err, "Should have been able to retrieve raw form of certificate")
	}
	tests.Passed("Should have been able to retrieve raw form of certificate")

	if !bytes.Equal(caKeyRaw, rcaKeyRaw) {
		tests.Failed("Should have matching certificate private key raw data between real and restored versions")
	}
	tests.Passed("Should have matching certificate private key raw data between real and restored versions")

	caConRaw, err := ca.Raw()
	if err != nil {
		tests.FailedWithError(err, "Should have being able to generate raw bytes of CertificateAuthority")
	}
	tests.Passed("Should have being able to generate raw bytes of CertificateAuthority")

	var newCA certificates.CertificateAuthority
	if err := newCA.FromRaw(caConRaw); err != nil {
		tests.FailedWithError(err, "Should have being able to load raw version of CertificateAuthority")
	}
	tests.Passed("Should have being able to load raw version of CertificateAuthority")

	rcaRaw, err = newCA.CertificateRaw()
	if err != nil {
		tests.FailedWithError(err, "Should have been able to retrieve raw form of certificate")
	}
	tests.Passed("Should have been able to retrieve raw form of certificate")

	if !bytes.Equal(caRaw, rcaRaw) {
		tests.Failed("Should have matching certificate raw data between real and restored versions")
	}
	tests.Passed("Should have matching certificate raw data between real and restored versions")

	rcaKeyRaw, err = newCA.PrivateKeyRaw()
	if err != nil {
		tests.FailedWithError(err, "Should have been able to retrieve raw form of certificate")
	}
	tests.Passed("Should have been able to retrieve raw form of certificate")

	if !bytes.Equal(caKeyRaw, rcaKeyRaw) {
		tests.Failed("Should have matching certificate private key raw data between real and restored versions")
	}
	tests.Passed("Should have matching certificate private key raw data between real and restored versions")
}
