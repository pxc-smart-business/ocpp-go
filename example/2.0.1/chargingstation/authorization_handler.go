package main

import (
	"github.com/pxc-smart-business/ocpp-go/ocpp2.0.1/authorization"
)

func (handler *ChargingStationHandler) OnClearCache(request *authorization.ClearCacheRequest) (response *authorization.ClearCacheResponse, err error) {
	logDefault(request.GetFeatureName()).Infof("cleared mocked cache")
	return authorization.NewClearCacheResponse(authorization.ClearCacheStatusAccepted), nil
}
