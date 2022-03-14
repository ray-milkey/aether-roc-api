// SPDX-FileCopyrightText: 2020-present Open Networking Foundation <info@opennetworking.org>
//
// SPDX-License-Identifier: Apache-2.0
//

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/ghodss/yaml"
	"github.com/labstack/echo/v4"
	aether_2_0_0 "github.com/onosproject/aether-roc-api/pkg/aether_2_0_0/server"
	aether_4_0_0 "github.com/onosproject/aether-roc-api/pkg/aether_4_0_0/server"
	app_gtwy "github.com/onosproject/aether-roc-api/pkg/app_gtwy/server"
	externalRef0 "github.com/onosproject/aether-roc-api/pkg/toplevel/types"
	"github.com/onosproject/onos-api/go/onos/config/diags"
	"github.com/onosproject/onos-lib-go/pkg/errors"
	"github.com/openconfig/gnmi/proto/gnmi"
	htmltemplate "html/template"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
)

// server-interface template override

import (
	"github.com/onosproject/aether-roc-api/pkg/southbound"
	"github.com/onosproject/aether-roc-api/pkg/utils"
	"github.com/onosproject/onos-lib-go/pkg/logging"
	"reflect"
)

// HTMLData -
type HTMLData struct {
	File        string
	Description string
}

const authorization = "Authorization"

// Implement the Server Interface for access to gNMI
var log = logging.GetLogger("toplevel")

// gnmiGetTargets returns a list of Targets.
func (i *TopLevelServer) gnmiGetTargets(ctx context.Context) (*externalRef0.TargetsNames, error) {
	gnmiGet := new(gnmi.GetRequest)
	gnmiGet.Encoding = gnmi.Encoding_PROTO
	gnmiGet.Path = make([]*gnmi.Path, 1)
	gnmiGet.Path[0] = &gnmi.Path{
		Target: "*",
	}

	log.Infof("gnmiGetRequest %s", gnmiGet.String())
	gnmiVal, err := utils.GetResponseUpdate(i.GnmiClient.Get(ctx, gnmiGet))
	if err != nil {
		return nil, err
	}
	gnmiLeafListStr, ok := gnmiVal.Value.(*gnmi.TypedValue_LeaflistVal)
	if !ok {
		return nil, fmt.Errorf("expecting a leaf list. Got %s", gnmiVal.String())
	}

	log.Infof("gNMI %s", gnmiLeafListStr.LeaflistVal.String())
	targetsNames := make(externalRef0.TargetsNames, 0)
	for _, elem := range gnmiLeafListStr.LeaflistVal.Element {
		targetName := elem.GetStringVal()
		targetsNames = append(targetsNames, externalRef0.TargetName{
			Name: &targetName,
		})
	}
	return &targetsNames, nil
}

// grpcGetTransactions returns a list of Transactions.
func (i *TopLevelServer) grpcGetTransactions(ctx context.Context) (*externalRef0.TransactionList, error) {
	log.Infof("grpcGetTransactions - subscribe=false")

	// At present (Jan '22) ListTransactions is not implemented - use ListNetworkChanges
	stream, err := i.ConfigClient.ListNetworkChanges(ctx, &diags.ListNetworkChangeRequest{
		Subscribe: false,
	})
	if err != nil {
		return nil, errors.FromGRPC(err)
	}
	transactionList := make(externalRef0.TransactionList, 0)
	for {
		networkChange, err := stream.Recv()
		if err == io.EOF || networkChange == nil {
			break
		}
		created := networkChange.GetChange().GetCreated()
		updated := networkChange.GetChange().GetUpdated()
		deleted := networkChange.GetChange().GetDeleted()
		username := networkChange.GetChange().GetUsername()

		status := struct {
			Phase externalRef0.TransactionStatusPhase
			State externalRef0.TransactionStatusState
		}{
			Phase: externalRef0.NewTransactionStatusPhase(int(networkChange.GetChange().GetStatus().Phase)),
			State: externalRef0.NewTransactionStatusState(int(networkChange.GetChange().GetStatus().State)),
		}

		transaction := externalRef0.Transaction{
			Id:       string(networkChange.GetChange().GetID()),
			Index:    int64(networkChange.GetChange().GetIndex()),
			Revision: int64((networkChange.GetChange().GetRevision())),
			Status: (*struct {
				Phase externalRef0.TransactionStatusPhase `json:"phase"`
				State externalRef0.TransactionStatusState `json:"state"`
			})(&status),
			Created:  &created,
			Updated:  &updated,
			Deleted:  &deleted,
			Username: &username,
		}
		changes := make([]externalRef0.Change, 0, len(networkChange.GetChange().GetChanges()))
		for _, networkChangeChange := range networkChange.GetChange().GetChanges() {
			targetType := string(networkChangeChange.GetDeviceType())
			targetVer := string(networkChangeChange.GetDeviceVersion())
			change := externalRef0.Change{
				TargetId:      string(networkChangeChange.GetDeviceID()),
				TargetType:    &targetType,
				TargetVersion: &targetVer,
			}

			changeValues := make([]externalRef0.ChangeValue, 0, len(networkChangeChange.GetValues()))
			for _, nccValue := range networkChangeChange.GetValues() {
				removed := nccValue.GetRemoved()
				value := nccValue.GetValue().ValueToString()
				changeValue := externalRef0.ChangeValue{
					Path:    nccValue.GetPath(),
					Removed: &removed,
					Value:   &value,
				}
				changeValues = append(changeValues, changeValue)
			}
			change.Values = &changeValues

			changes = append(changes, change)
		}
		transaction.Changes = &changes

		transactionList = append(transactionList, transaction)
	}

	return &transactionList, nil
}

// TopLevelServer -
type TopLevelServer struct {
	GnmiClient    southbound.GnmiClient
	GnmiTimeout   time.Duration
	ConfigClient  diags.ChangeServiceClient
	Authorization bool
}

// PatchAetherRocAPI impl of gNMI access at /aether-roc-api
func (i *TopLevelServer) PatchAetherRocAPI(ctx echo.Context) error {

	var response interface{}
	var err error

	gnmiCtx, cancel := utils.NewGnmiContext(ctx, i.GnmiTimeout)
	defer cancel()

	// Response patched
	body, err := utils.ReadRequestBody(ctx.Request().Body)
	if err != nil {
		return err
	}
	response, err = i.gnmiPatchAetherRocAPI(gnmiCtx, body, "/aether-roc-api")
	if err != nil {
		return utils.ConvertGrpcError(err)
	}
	// It's not enough to check if response==nil - see https://medium.com/@glucn/golang-an-interface-holding-a-nil-value-is-not-nil-bb151f472cc7
	if reflect.ValueOf(response).Kind() == reflect.Ptr && reflect.ValueOf(response).IsNil() {
		return echo.NewHTTPError(http.StatusNotFound)
	}

	log.Infof("PatchAetherRocAPI")
	return ctx.JSON(http.StatusOK, response)
}

// GetTargets -
func (i *TopLevelServer) GetTargets(ctx echo.Context) error {
	var response interface{}
	var err error

	gnmiCtx, cancel := utils.NewGnmiContext(ctx, i.GnmiTimeout)
	defer cancel()

	// Response GET OK 200
	response, err = i.gnmiGetTargets(gnmiCtx)
	if err != nil {
		return utils.ConvertGrpcError(err)
	}
	log.Infof("GetTargets")
	return ctx.JSON(http.StatusOK, response)
}

// GetTransactions -
func (i *TopLevelServer) GetTransactions(ctx echo.Context) error {
	var response interface{}
	var err error

	gnmiCtx, cancel := utils.NewGnmiContext(ctx, i.GnmiTimeout)
	defer cancel()

	// Response GET OK 200
	response, err = i.grpcGetTransactions(gnmiCtx)
	if err != nil {
		return utils.ConvertGrpcError(err)
	}
	log.Infof("GetTransactions")
	return ctx.JSON(http.StatusOK, response)
}

// PostSdcoreSynchronize -
func (i *TopLevelServer) PostSdcoreSynchronize(httpContext echo.Context) error {

	// Response GET OK 200
	if i.Authorization {
		if err := checkAuthorization(httpContext, "AetherROCAdmin"); err != nil {
			return err
		}
	}

	address := fmt.Sprintf("http://%s:8080/synchronize", httpContext.Param("service"))
	resp, err := http.Post(address, "application/json", nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("error calling %s. %v", address, err))
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("error reading body %s. %v", address, err))
	}

	log.Infof("PostSdcoreSynchronize to %s %s %s", httpContext.Param("service"), resp.Status, string(body))
	respStruct := struct {
		Response string `json:"response"`
	}{Response: string(body)}
	return httpContext.JSON(resp.StatusCode, &respStruct)
}

// GetSpec -
func (i *TopLevelServer) GetSpec(ctx echo.Context) error {
	response, err := GetSwagger()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err)
	}
	log.Infof("GetSpec")
	return acceptTypes(ctx, response)
}

// GetAether200Spec -
func (i *TopLevelServer) GetAether200Spec(ctx echo.Context) error {
	response, err := aether_2_0_0.GetSwagger()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err)
	}
	return acceptTypes(ctx, response)
}

// GetAether400Spec -
func (i *TopLevelServer) GetAether400Spec(ctx echo.Context) error {
	response, err := aether_4_0_0.GetSwagger()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err)
	}
	return acceptTypes(ctx, response)
}

// GetAetherAppGtwySpec -
func (i *TopLevelServer) GetAetherAppGtwySpec(ctx echo.Context) error {
	response, err := app_gtwy.GetSwagger()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err)
	}
	return acceptTypes(ctx, response)
}

func acceptTypes(ctx echo.Context, response *openapi3.T) error {
	acceptType := ctx.Request().Header.Get("Accept")

	if strings.Contains(acceptType, "application/json") {
		return ctx.JSONPretty(http.StatusOK, response, "  ")
	} else if strings.Contains(acceptType, "text/html") {
		templateText, err := ioutil.ReadFile("assets/html-page.tpl")
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "unable to load template %s", err)
		}
		specTemplate, err := htmltemplate.New("spectemplate").Parse(string(templateText))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "error parsing template %s", err)
		}
		var b bytes.Buffer
		_ = specTemplate.Execute(&b, HTMLData{
			File:        ctx.Request().RequestURI[1:],
			Description: "Aether ROC API",
		})
		ctx.Response().Header().Set("Content-Type", "text/html")
		return ctx.HTMLBlob(http.StatusOK, b.Bytes())
	} else if strings.Contains(acceptType, "application/yaml") || strings.Contains(acceptType, "*/*") {
		jsonFirst, err := json.Marshal(response)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err)
		}
		yamlResp, err := yaml.JSONToYAML(jsonFirst)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err)
		}
		ctx.Response().Header().Set("Content-Type", "application/yaml")
		return ctx.HTMLBlob(http.StatusOK, yamlResp)
	}
	return echo.NewHTTPError(http.StatusNotImplemented,
		fmt.Sprintf("only application/yaml, application/json and text/html encoding supported. "+
			"No match for %s", acceptType))
}

// register template override
