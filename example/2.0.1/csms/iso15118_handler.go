package main

import (
	"github.com/pxc-smart-business/ocpp-go/ocpp"
	"github.com/pxc-smart-business/ocpp-go/ocpp2.0.1/iso15118"
	"github.com/pxc-smart-business/ocpp-go/ocppj"
)

func (c *CSMSHandler) OnGet15118EVCertificate(chargingStationID string, request *iso15118.Get15118EVCertificateRequest) (response *iso15118.Get15118EVCertificateResponse, err error) {
	logDefault(chargingStationID, request.GetFeatureName()).Warnf("Unsupported feature")
	return nil, ocpp.NewError(ocppj.NotSupported, "Not supported", "")
}

func (c *CSMSHandler) OnGetCertificateStatus(chargingStationID string, request *iso15118.GetCertificateStatusRequest) (response *iso15118.GetCertificateStatusResponse, err error) {
	logDefault(chargingStationID, request.GetFeatureName()).Warnf("Unsupported feature")
	return nil, ocpp.NewError(ocppj.NotSupported, "Not supported", "")
}
