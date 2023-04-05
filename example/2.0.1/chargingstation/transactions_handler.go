package main

import (
	"github.com/pxc-smart-business/ocpp-go/ocpp"
	"github.com/pxc-smart-business/ocpp-go/ocpp2.0.1/transactions"
	"github.com/pxc-smart-business/ocpp-go/ocppj"
)

func (handler *ChargingStationHandler) OnGetTransactionStatus(request *transactions.GetTransactionStatusRequest) (response *transactions.GetTransactionStatusResponse, err error) {
	logDefault(request.GetFeatureName()).Warnf("Unsupported feature")
	return nil, ocpp.NewError(ocppj.NotSupported, "Not supported", "")
}
