package main

import (
	"github.com/pxc-smart-business/ocpp-go/ocpp"
	"github.com/pxc-smart-business/ocpp-go/ocpp2.0.1/security"
	"github.com/pxc-smart-business/ocpp-go/ocppj"
)

func (c *CSMSHandler) OnSecurityEventNotification(chargingStationID string, request *security.SecurityEventNotificationRequest) (response *security.SecurityEventNotificationResponse, err error) {
	logDefault(chargingStationID, request.GetFeatureName()).Infof("type: %s, info: %s", request.Type, request.TechInfo)
	response = security.NewSecurityEventNotificationResponse()
	return
}

func (c *CSMSHandler) OnSignCertificate(chargingStationID string, request *security.SignCertificateRequest) (response *security.SignCertificateResponse, err error) {
	logDefault(chargingStationID, request.GetFeatureName()).Warnf("Unsupported feature")
	return nil, ocpp.NewError(ocppj.NotSupported, "Not supported", "")
}
