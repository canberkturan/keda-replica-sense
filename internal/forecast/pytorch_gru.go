package forecast

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

// PyTorchGRUFeatureSchema is shared by the CPU PyTorch trainer and the Go
// forecaster. The artifact contains tensors only; Go never loads pickle,
// TorchScript, or any other executable model representation.
const PyTorchGRUFeatureSchema = "demand-calendar-sequence-pytorch-v1"

const pytorchGRUFormat = "replicasense-pytorch-gru-v1"

type pytorchGRUArtifact struct {
	Engine         string                `json:"engine"`
	Format         string                `json:"format"`
	FeatureSchema  string                `json:"feature_schema"`
	UpperQuantile  float64               `json:"upper_quantile"`
	Sequence       int                   `json:"sequence_length"`
	Inputs         int                   `json:"input_size"`
	Hidden         int                   `json:"hidden_size"`
	ValueMinimum   float64               `json:"value_minimum"`
	ValueScale     float64               `json:"value_scale"`
	P95Calibration float64               `json:"p95_calibration"`
	P50            pytorchGRUNetworkData `json:"p50"`
	P95            pytorchGRUNetworkData `json:"p95"`
}

// pytorchGRUNetworkData is the public-tensor subset of torch.nn.GRU with one
// layer and batch_first=true. PyTorch stores gates in reset, update, new order.
type pytorchGRUNetworkData struct {
	WeightIH []float64 `json:"weight_ih_l0"`
	WeightHH []float64 `json:"weight_hh_l0"`
	BiasIH   []float64 `json:"bias_ih_l0"`
	BiasHH   []float64 `json:"bias_hh_l0"`
	Head     []float64 `json:"head_weight"`
	HeadBias float64   `json:"head_bias"`
}

type pytorchGRUModel struct {
	p50, p95       pytorchGRUNetworkData
	upperQuantile  float64
	sequenceLength int
	inputs, hidden int
	minimum, scale float64
	p95Calibration float64
}

func (m *pytorchGRUModel) Engine() string               { return "gru" }
func (m *pytorchGRUModel) OperationalQuantile() float64 { return m.upperQuantile }

func (m *pytorchGRUModel) PredictSamples(samples []domain.Sample, request Request) (Prediction, error) {
	if m == nil || m.sequenceLength < 1 || m.inputs != gruInputSize || m.hidden < 1 || m.scale <= 0 {
		return Prediction{}, fmt.Errorf("pytorch gru model is unavailable")
	}
	inputs, err := gruInputs(samples, len(samples)-1, m.sequenceLength, m.minimum, m.scale, request.BusinessLocation)
	if err != nil {
		return Prediction{}, err
	}
	p50Normalized := m.p50.forward(inputs, m.inputs, m.hidden)
	p95Normalized := m.p95.forward(inputs, m.inputs, m.hidden)
	if !finiteGRU(p50Normalized) || !finiteGRU(p95Normalized) {
		return Prediction{}, fmt.Errorf("pytorch gru produced a non-finite normalized prediction")
	}
	p50 := math.Max(0, p50Normalized*m.scale+m.minimum)
	p95 := math.Max(p50, p95Normalized*m.scale+m.minimum+m.p95Calibration)
	if !finiteGRU(p50) || !finiteGRU(p95) {
		return Prediction{}, fmt.Errorf("pytorch gru produced a non-finite prediction")
	}
	return Prediction{P50: p50, P95: p95}, nil
}

func (n pytorchGRUNetworkData) forward(inputs [][]float64, inputSize, hiddenSize int) float64 {
	hidden := make([]float64, hiddenSize)
	for _, input := range inputs {
		next := make([]float64, hiddenSize)
		for row := 0; row < hiddenSize; row++ {
			reset := sigmoid(n.gate(0, row, input, hidden, inputSize, hiddenSize))
			update := sigmoid(n.gate(1, row, input, hidden, inputSize, hiddenSize))
			candidate := math.Tanh(n.candidate(row, input, hidden, reset, inputSize, hiddenSize))
			next[row] = (1-update)*candidate + update*hidden[row]
		}
		hidden = next
	}
	output := n.HeadBias
	for i, value := range hidden {
		output += n.Head[i] * value
	}
	return output
}

func (n pytorchGRUNetworkData) gate(gate, row int, input, hidden []float64, inputSize, hiddenSize int) float64 {
	index := gate*hiddenSize + row
	value := n.BiasIH[index] + n.BiasHH[index]
	for column := 0; column < inputSize; column++ {
		value += n.WeightIH[index*inputSize+column] * input[column]
	}
	for column := 0; column < hiddenSize; column++ {
		value += n.WeightHH[index*hiddenSize+column] * hidden[column]
	}
	return value
}

func (n pytorchGRUNetworkData) candidate(row int, input, hidden []float64, reset float64, inputSize, hiddenSize int) float64 {
	index := 2*hiddenSize + row
	value := n.BiasIH[index]
	for column := 0; column < inputSize; column++ {
		value += n.WeightIH[index*inputSize+column] * input[column]
	}
	hiddenValue := n.BiasHH[index]
	for column := 0; column < hiddenSize; column++ {
		hiddenValue += n.WeightHH[index*hiddenSize+column] * hidden[column]
	}
	return value + reset*hiddenValue
}

func loadPyTorchGRUArtifact(id string, artifactBytes []byte) (Model, error) {
	var artifact pytorchGRUArtifact
	if err := json.Unmarshal(artifactBytes, &artifact); err != nil {
		return nil, fmt.Errorf("decode pytorch gru artifact %s: %w", id, err)
	}
	if artifact.Engine != "gru" || artifact.Format != pytorchGRUFormat || artifact.FeatureSchema != PyTorchGRUFeatureSchema || artifact.Sequence < gruSequenceLength || artifact.Inputs != gruInputSize || artifact.Hidden < 1 || artifact.UpperQuantile <= 0 || artifact.UpperQuantile >= 1 || !finiteGRU(artifact.ValueMinimum) || !finiteGRU(artifact.ValueScale) || !finiteGRU(artifact.P95Calibration) || artifact.ValueScale <= 0 || !validPyTorchGRUNetwork(artifact.P50, artifact.Inputs, artifact.Hidden) || !validPyTorchGRUNetwork(artifact.P95, artifact.Inputs, artifact.Hidden) {
		return nil, fmt.Errorf("invalid pytorch gru artifact %s", id)
	}
	return &pytorchGRUModel{p50: artifact.P50, p95: artifact.P95, upperQuantile: artifact.UpperQuantile, sequenceLength: artifact.Sequence, inputs: artifact.Inputs, hidden: artifact.Hidden, minimum: artifact.ValueMinimum, scale: artifact.ValueScale, p95Calibration: artifact.P95Calibration}, nil
}

func validPyTorchGRUNetwork(network pytorchGRUNetworkData, inputs, hidden int) bool {
	if len(network.WeightIH) != 3*hidden*inputs || len(network.WeightHH) != 3*hidden*hidden || len(network.BiasIH) != 3*hidden || len(network.BiasHH) != 3*hidden || len(network.Head) != hidden || !finiteGRU(network.HeadBias) {
		return false
	}
	for _, values := range [][]float64{network.WeightIH, network.WeightHH, network.BiasIH, network.BiasHH, network.Head} {
		for _, value := range values {
			if !finiteGRU(value) {
				return false
			}
		}
	}
	return true
}
