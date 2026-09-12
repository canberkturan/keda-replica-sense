//go:build xgboost

// Package xgboost is the optional native XGBoost C API boundary.
package xgboost

/*
#cgo CFLAGS: -I/opt/xgboost/include
#cgo LDFLAGS: -L/usr/local/lib -Wl,-rpath,/usr/local/lib -lxgboost
#include <stdlib.h>
#include <xgboost/c_api.h>
*/
import "C"

import (
	"fmt"
	"math"
	"unsafe"
)

// Version proves that the dynamically packaged native library is callable
// before training/inference bindings are enabled.
func Version() (string, error) {
	var major, minor, patch C.int
	C.XGBoostVersion(&major, &minor, &patch)
	return fmt.Sprintf("%d.%d.%d", int(major), int(minor), int(patch)), nil
}

// Model owns one native Booster handle.
type Model struct{ handle C.BoosterHandle }

// TrainOptions controls a CPU regression booster.  The defaults deliberately
// favour deterministic, modest models suitable for control-plane forecasts.
type TrainOptions struct {
	Rounds        int
	MaxDepth      int
	Eta           float64
	Objective     string
	QuantileAlpha float64
}

// Train fits a squared-error gradient-boosted tree model using row-major
// features. Each feature row has exactly columns values and one target label.
func Train(features, labels []float32, rows, columns int, options TrainOptions) (*Model, error) {
	if rows <= 0 || columns <= 0 {
		return nil, fmt.Errorf("xgboost training dimensions must be positive")
	}
	if len(features) != rows*columns {
		return nil, fmt.Errorf("xgboost features length %d, want %d", len(features), rows*columns)
	}
	if len(labels) != rows {
		return nil, fmt.Errorf("xgboost labels length %d, want %d", len(labels), rows)
	}
	for _, values := range [][]float32{features, labels} {
		for _, value := range values {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, fmt.Errorf("xgboost training data contains a non-finite value")
			}
		}
	}
	if options.Rounds <= 0 {
		options.Rounds = 50
	}
	if options.MaxDepth <= 0 {
		options.MaxDepth = 4
	}
	if options.Eta <= 0 {
		options.Eta = 0.1
	}

	matrix, err := newMatrix(features, rows, columns)
	if err != nil {
		return nil, err
	}
	defer matrix.close()
	if err := matrix.setLabels(labels); err != nil {
		return nil, err
	}

	model, err := newWithMatrix(matrix.handle)
	if err != nil {
		return nil, err
	}
	objective := options.Objective
	if objective == "" {
		objective = "reg:squarederror"
	}
	params := map[string]string{
		"objective": objective, "booster": "gbtree", "tree_method": "hist",
		"max_depth": fmt.Sprintf("%d", options.MaxDepth), "eta": fmt.Sprintf("%.8g", options.Eta),
		"seed": "0", "nthread": "1", "verbosity": "0", "num_feature": fmt.Sprintf("%d", columns),
	}
	if objective == "reg:quantileerror" {
		if options.QuantileAlpha <= 0 || options.QuantileAlpha >= 1 {
			model.Close()
			return nil, fmt.Errorf("quantile alpha must be between zero and one")
		}
		params["quantile_alpha"] = fmt.Sprintf("%.8g", options.QuantileAlpha)
	}
	for name, value := range params {
		if err := model.SetParam(name, value); err != nil {
			model.Close()
			return nil, err
		}
	}
	for iteration := 0; iteration < options.Rounds; iteration++ {
		if code := C.XGBoosterUpdateOneIter(model.handle, C.int(iteration), matrix.handle); code != 0 {
			model.Close()
			return nil, nativeError("train booster")
		}
	}
	return model, nil
}

func New() (*Model, error) {
	return newWithMatrix(nil)
}

func newWithMatrix(matrix C.DMatrixHandle) (*Model, error) {
	var handle C.BoosterHandle
	var matrices *C.DMatrixHandle
	var count C.bst_ulong
	if matrix != nil {
		matrices, count = &matrix, 1
	}
	if code := C.XGBoosterCreate(matrices, count, &handle); code != 0 {
		return nil, nativeError("create booster")
	}
	return &Model{handle: handle}, nil
}

// Predict returns one prediction per row of the row-major feature matrix.
func (m *Model) Predict(features []float32, rows, columns int) ([]float32, error) {
	if m == nil || m.handle == nil {
		return nil, fmt.Errorf("xgboost model is closed")
	}
	if rows <= 0 || columns <= 0 || len(features) != rows*columns {
		return nil, fmt.Errorf("xgboost prediction dimensions do not match features")
	}
	matrix, err := newMatrix(features, rows, columns)
	if err != nil {
		return nil, err
	}
	defer matrix.close()
	var outputLength C.bst_ulong
	var output *C.float
	if code := C.XGBoosterPredict(m.handle, matrix.handle, 0, 0, 0, &outputLength, &output); code != 0 {
		return nil, nativeError("predict")
	}
	if int(outputLength) != rows {
		return nil, fmt.Errorf("xgboost returned %d predictions, want %d", int(outputLength), rows)
	}
	return unsafe.Slice((*float32)(unsafe.Pointer(output)), rows), nil
}

func LoadArtifact(artifact []byte) (*Model, error) {
	if len(artifact) == 0 {
		return nil, fmt.Errorf("xgboost artifact is empty")
	}
	model, err := New()
	if err != nil {
		return nil, err
	}
	if code := C.XGBoosterLoadModelFromBuffer(model.handle, unsafe.Pointer(&artifact[0]), C.bst_ulong(len(artifact))); code != 0 {
		model.Close()
		return nil, nativeError("load model artifact")
	}
	return model, nil
}

func (m *Model) Artifact() ([]byte, error) {
	if m == nil || m.handle == nil {
		return nil, fmt.Errorf("xgboost model is closed")
	}
	config := C.CString(`{"format":"ubj"}`)
	defer C.free(unsafe.Pointer(config))
	var size C.bst_ulong
	var data *C.char
	if code := C.XGBoosterSaveModelToBuffer(m.handle, config, &size, &data); code != 0 {
		return nil, nativeError("save model artifact")
	}
	return C.GoBytes(unsafe.Pointer(data), C.int(size)), nil
}

func (m *Model) SetParam(name, value string) error {
	if m == nil || m.handle == nil {
		return fmt.Errorf("xgboost model is closed")
	}
	key, setting := C.CString(name), C.CString(value)
	defer C.free(unsafe.Pointer(key))
	defer C.free(unsafe.Pointer(setting))
	if C.XGBoosterSetParam(m.handle, key, setting) != 0 {
		return nativeError("set parameter")
	}
	return nil
}

func (m *Model) Close() {
	if m != nil && m.handle != nil {
		C.XGBoosterFree(m.handle)
		m.handle = nil
	}
}

func nativeError(operation string) error {
	return fmt.Errorf("%s: %s", operation, C.GoString(C.XGBGetLastError()))
}

type matrix struct{ handle C.DMatrixHandle }

func newMatrix(values []float32, rows, columns int) (*matrix, error) {
	var handle C.DMatrixHandle
	if code := C.XGDMatrixCreateFromMat((*C.float)(unsafe.Pointer(&values[0])), C.bst_ulong(rows), C.bst_ulong(columns), C.float(float32(math.NaN())), &handle); code != 0 {
		return nil, nativeError("create matrix")
	}
	return &matrix{handle: handle}, nil
}

func (m *matrix) setLabels(labels []float32) error {
	field := C.CString("label")
	defer C.free(unsafe.Pointer(field))
	if code := C.XGDMatrixSetFloatInfo(m.handle, field, (*C.float)(unsafe.Pointer(&labels[0])), C.bst_ulong(len(labels))); code != 0 {
		return nativeError("set matrix labels")
	}
	return nil
}

func (m *matrix) close() {
	if m != nil && m.handle != nil {
		C.XGDMatrixFree(m.handle)
		m.handle = nil
	}
}
