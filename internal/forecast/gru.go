package forecast

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/canberkturan/keda-replica-sense/internal/domain"
)

// GRUFeatureSchema is the sequence input contract for stored GRU artifacts.
// It combines observed demand with calendar fields known at prediction time.
const GRUFeatureSchema = "demand-calendar-sequence-v1"

const (
	gruFormat         = "replicasense-gru-v1"
	gruSequenceLength = 60
	gruInputSize      = 10 // demand, clock, weekday, day-of-month, month, weekend
	gruHiddenSize     = 12
)

type gruArtifact struct {
	Engine        string     `json:"engine"`
	Format        string     `json:"format"`
	FeatureSchema string     `json:"feature_schema"`
	UpperQuantile float64    `json:"upper_quantile"`
	Sequence      int        `json:"sequence_length"`
	Inputs        int        `json:"input_size"`
	Hidden        int        `json:"hidden_size"`
	ValueMinimum  float64    `json:"value_minimum"`
	ValueScale    float64    `json:"value_scale"`
	P50           gruWeights `json:"p50"`
	P95           gruWeights `json:"p95"`
}

// gruWeights is deliberately a JSON-only numeric artifact. It does not use a
// deserializer capable of executing code, such as Python pickle.
type gruWeights struct {
	UpdateInput  []float64 `json:"update_input"`
	UpdateHidden []float64 `json:"update_hidden"`
	UpdateBias   []float64 `json:"update_bias"`
	ResetInput   []float64 `json:"reset_input"`
	ResetHidden  []float64 `json:"reset_hidden"`
	ResetBias    []float64 `json:"reset_bias"`
	StateInput   []float64 `json:"state_input"`
	StateHidden  []float64 `json:"state_hidden"`
	StateBias    []float64 `json:"state_bias"`
	Output       []float64 `json:"output"`
	OutputBias   float64   `json:"output_bias"`
}

type gruNetwork struct {
	weights gruWeights
	hidden  int
	inputs  int
}

type gruModel struct {
	p50, p95       gruNetwork
	upperQuantile  float64
	sequenceLength int
	minimum, scale float64
}

func (m *gruModel) Engine() string               { return "gru" }
func (m *gruModel) OperationalQuantile() float64 { return m.upperQuantile }

func (m *gruModel) PredictSamples(samples []domain.Sample, request Request) (Prediction, error) {
	if m == nil || m.sequenceLength < 1 || m.scale <= 0 {
		return Prediction{}, fmt.Errorf("gru model is unavailable")
	}
	inputs, err := gruInputs(samples, len(samples)-1, m.sequenceLength, m.minimum, m.scale, request.BusinessLocation)
	if err != nil {
		return Prediction{}, err
	}
	p50Normalized, _ := m.p50.forward(inputs)
	p95Normalized, _ := m.p95.forward(inputs)
	if !finiteGRU(p50Normalized) || !finiteGRU(p95Normalized) {
		return Prediction{}, fmt.Errorf("gru produced a non-finite normalized prediction")
	}
	p50 := math.Max(0, p50Normalized*m.scale+m.minimum)
	p95 := math.Max(p50, p95Normalized*m.scale+m.minimum)
	if !finiteGRU(p50) || !finiteGRU(p95) {
		return Prediction{}, fmt.Errorf("gru produced a non-finite prediction")
	}
	return Prediction{P50: p50, P95: p95}, nil
}

type gruTrainingConfig struct {
	Epochs       int
	MaxExamples  int
	BatchSize    int
	LearningRate float64
}

var finalGRUTraining = gruTrainingConfig{Epochs: 8, MaxExamples: 4096, BatchSize: 16, LearningRate: .003}
var validationGRUTraining = gruTrainingConfig{Epochs: 3, MaxExamples: 512, BatchSize: 16, LearningRate: .003}

// TrainGRU fits independent median and upper-quantile recurrent networks to
// the maximum demand in the configured future horizon. Every input sequence
// contains only observations available at its decision time.
func TrainGRU(samples []domain.Sample, request Request) (Model, []byte, error) {
	model, err := trainGRU(samples, request, finalGRUTraining)
	if err != nil {
		return nil, nil, err
	}
	artifact, err := json.Marshal(gruArtifact{
		Engine: "gru", Format: gruFormat, FeatureSchema: GRUFeatureSchema,
		UpperQuantile: operationalQuantile(request), Sequence: model.sequenceLength,
		Inputs: model.p50.inputs, Hidden: model.p50.hidden, ValueMinimum: model.minimum,
		ValueScale: model.scale, P50: model.p50.weights, P95: model.p95.weights,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("encode gru artifact: %w", err)
	}
	return model, artifact, nil
}

func trainGRU(samples []domain.Sample, request Request, config gruTrainingConfig) (*gruModel, error) {
	if request.Horizon <= 0 || request.SamplingInterval <= 0 || request.Horizon%request.SamplingInterval != 0 {
		return nil, fmt.Errorf("gru requires a positive horizon that is a whole sampling interval")
	}
	steps := int(request.Horizon / request.SamplingInterval)
	if len(samples) <= gruSequenceLength+steps {
		return nil, fmt.Errorf("gru has %d samples, need more than %d", len(samples), gruSequenceLength+steps)
	}
	minimum, scale, err := gruNormalizer(samples)
	if err != nil {
		return nil, err
	}
	examples := gruExamples(samples, steps, gruSequenceLength)
	if len(examples) == 0 {
		return nil, fmt.Errorf("gru has no causal training examples")
	}
	examples = selectGRUExamples(examples, config.MaxExamples)
	location := request.BusinessLocation
	if location == nil {
		location = time.UTC
	}
	p50, err := trainGRUNetwork(samples, examples, minimum, scale, location, .50, config, 0x75ad39)
	if err != nil {
		return nil, fmt.Errorf("train gru median: %w", err)
	}
	p95, err := trainGRUNetwork(samples, examples, minimum, scale, location, operationalQuantile(request), config, 0xa8d413)
	if err != nil {
		return nil, fmt.Errorf("train gru upper quantile: %w", err)
	}
	return &gruModel{p50: p50, p95: p95, upperQuantile: operationalQuantile(request), sequenceLength: gruSequenceLength, minimum: minimum, scale: scale}, nil
}

// ValidateGRU uses expanding, walk-forward windows. Each validation model is
// fitted exclusively to observations before its cut, preventing future leakage.
func ValidateGRU(samples []domain.Sample, request Request, maxWindows int) (ValidationMetrics, error) {
	if request.Horizon <= 0 || request.SamplingInterval <= 0 || request.Horizon%request.SamplingInterval != 0 {
		return ValidationMetrics{}, fmt.Errorf("gru requires a positive horizon that is a whole sampling interval")
	}
	steps := int(request.Horizon / request.SamplingInterval)
	start := gruSequenceLength + steps + 1
	if len(samples) <= start+steps {
		return ValidationMetrics{}, fmt.Errorf("insufficient samples for gru walk-forward validation")
	}
	if maxWindows <= 0 {
		maxWindows = 12
	}
	if len(samples)-steps-start+1 > maxWindows {
		start = len(samples) - steps - maxWindows + 1
	}
	var metrics ValidationMetrics
	for cut := start; cut+steps <= len(samples); cut++ {
		model, err := trainGRU(samples[:cut], request, validationGRUTraining)
		if err != nil {
			return ValidationMetrics{}, err
		}
		prediction, err := model.PredictSamples(samples[:cut], request)
		if err != nil {
			return ValidationMetrics{}, err
		}
		actual := gruHorizonMaximum(samples, cut, steps)
		metrics.Windows++
		if prediction.P95 >= actual {
			metrics.Coverage++
		} else {
			metrics.UnderpredictionRate++
		}
		metrics.MeanAbsoluteError += math.Abs(prediction.P95 - actual)
		quantile := operationalQuantile(request)
		if actual >= prediction.P95 {
			metrics.PinballLossP95 += quantile * (actual - prediction.P95)
		} else {
			metrics.PinballLossP95 += (1 - quantile) * (prediction.P95 - actual)
		}
	}
	if metrics.Windows == 0 {
		return ValidationMetrics{}, fmt.Errorf("no gru validation windows")
	}
	count := float64(metrics.Windows)
	metrics.Coverage /= count
	metrics.UnderpredictionRate /= count
	metrics.MeanAbsoluteError /= count
	metrics.PinballLossP95 /= count
	return metrics, nil
}

func loadGRUArtifact(id string, artifactBytes []byte) (Model, error) {
	var artifact gruArtifact
	if err := json.Unmarshal(artifactBytes, &artifact); err != nil {
		return nil, fmt.Errorf("decode gru artifact %s: %w", id, err)
	}
	if artifact.Engine != "gru" || artifact.Format != gruFormat || artifact.FeatureSchema != GRUFeatureSchema || artifact.Sequence != gruSequenceLength || artifact.Inputs != gruInputSize || artifact.Hidden != gruHiddenSize || artifact.UpperQuantile <= 0 || artifact.UpperQuantile >= 1 || !finiteGRU(artifact.ValueMinimum) || !finiteGRU(artifact.ValueScale) || artifact.ValueScale <= 0 || !validGRUWeights(artifact.P50, artifact.Inputs, artifact.Hidden) || !validGRUWeights(artifact.P95, artifact.Inputs, artifact.Hidden) {
		return nil, fmt.Errorf("invalid gru artifact %s", id)
	}
	return &gruModel{p50: gruNetwork{weights: artifact.P50, inputs: artifact.Inputs, hidden: artifact.Hidden}, p95: gruNetwork{weights: artifact.P95, inputs: artifact.Inputs, hidden: artifact.Hidden}, upperQuantile: artifact.UpperQuantile, sequenceLength: artifact.Sequence, minimum: artifact.ValueMinimum, scale: artifact.ValueScale}, nil
}

type gruExample struct {
	end    int
	target float64
}

func gruExamples(samples []domain.Sample, horizonSteps, sequenceLength int) []gruExample {
	capacity := len(samples) - sequenceLength - horizonSteps
	if capacity < 0 {
		capacity = 0
	}
	examples := make([]gruExample, 0, capacity)
	for end := sequenceLength - 1; end+horizonSteps < len(samples); end++ {
		examples = append(examples, gruExample{end: end, target: gruHorizonMaximum(samples, end+1, horizonSteps)})
	}
	return examples
}

func selectGRUExamples(examples []gruExample, maximum int) []gruExample {
	if maximum <= 0 || len(examples) <= maximum {
		return examples
	}
	if maximum == 1 {
		return []gruExample{examples[len(examples)-1]}
	}
	selected := make([]gruExample, 0, maximum)
	for i := 0; i < maximum; i++ {
		index := i * (len(examples) - 1) / (maximum - 1)
		selected = append(selected, examples[index])
	}
	return selected
}

func gruHorizonMaximum(samples []domain.Sample, start, steps int) float64 {
	maximum := samples[start].ObservedValue
	for _, sample := range samples[start+1 : start+steps] {
		if sample.ObservedValue > maximum {
			maximum = sample.ObservedValue
		}
	}
	return maximum
}

func gruNormalizer(samples []domain.Sample) (float64, float64, error) {
	minimum, maximum := 0.0, 0.0
	for i, sample := range samples {
		if !finiteGRU(sample.ObservedValue) {
			return 0, 0, fmt.Errorf("gru sample %d is non-finite", i)
		}
		if i == 0 || sample.ObservedValue < minimum {
			minimum = sample.ObservedValue
		}
		if i == 0 || sample.ObservedValue > maximum {
			maximum = sample.ObservedValue
		}
	}
	scale := maximum - minimum
	if scale < 1e-9 {
		scale = 1
	}
	return minimum, scale, nil
}

func gruInputs(samples []domain.Sample, end, sequenceLength int, minimum, scale float64, location *time.Location) ([][]float64, error) {
	if end < sequenceLength-1 || end >= len(samples) || scale <= 0 || !finiteGRU(minimum) || !finiteGRU(scale) {
		return nil, fmt.Errorf("insufficient samples for gru sequence")
	}
	if location == nil {
		location = time.UTC
	}
	inputs := make([][]float64, sequenceLength)
	for offset := 0; offset < sequenceLength; offset++ {
		sample := samples[end-sequenceLength+1+offset]
		if !finiteGRU(sample.ObservedValue) {
			return nil, fmt.Errorf("gru input sample is non-finite")
		}
		inputs[offset] = gruInput(sample, minimum, scale, location)
	}
	return inputs, nil
}

func gruInput(sample domain.Sample, minimum, scale float64, location *time.Location) []float64 {
	t := sample.ObservedAt.In(location)
	minuteOfDay := float64(t.Hour()*60+t.Minute()) / (24 * 60)
	weekday := float64(t.Weekday()) / 7
	days := float64(daysInMonth(t.Year(), t.Month()))
	dayOfMonth := float64(t.Day()-1) / days
	month := float64(t.Month()-1) / 12
	weekend := 0.0
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		weekend = 1
	}
	return []float64{
		(sample.ObservedValue - minimum) / scale,
		math.Sin(2 * math.Pi * minuteOfDay), math.Cos(2 * math.Pi * minuteOfDay),
		math.Sin(2 * math.Pi * weekday), math.Cos(2 * math.Pi * weekday),
		math.Sin(2 * math.Pi * dayOfMonth), math.Cos(2 * math.Pi * dayOfMonth),
		math.Sin(2 * math.Pi * month), math.Cos(2 * math.Pi * month),
		weekend,
	}
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func trainGRUNetwork(samples []domain.Sample, examples []gruExample, minimum, scale float64, location *time.Location, quantile float64, config gruTrainingConfig, seed uint64) (gruNetwork, error) {
	if config.Epochs <= 0 || config.BatchSize <= 0 || config.LearningRate <= 0 || quantile <= 0 || quantile >= 1 {
		return gruNetwork{}, fmt.Errorf("invalid gru training configuration")
	}
	network := gruNetwork{weights: newGRUWeights(gruInputSize, gruHiddenSize, seed), inputs: gruInputSize, hidden: gruHiddenSize}
	optimizer := newGRUAdam(gruInputSize, gruHiddenSize)
	step := 0
	for epoch := 0; epoch < config.Epochs; epoch++ {
		for first := 0; first < len(examples); first += config.BatchSize {
			last := first + config.BatchSize
			if last > len(examples) {
				last = len(examples)
			}
			gradient := zeroGRUWeights(network.inputs, network.hidden)
			for _, example := range examples[first:last] {
				inputs, err := gruInputs(samples, example.end, gruSequenceLength, minimum, scale, location)
				if err != nil {
					return gruNetwork{}, err
				}
				prediction, states := network.forward(inputs)
				target := (example.target - minimum) / scale
				outputGradient := 1 - quantile
				if target >= prediction {
					outputGradient = -quantile
				}
				network.backward(inputs, states, outputGradient, &gradient)
			}
			scaleGRUWeights(&gradient, 1/float64(last-first))
			clipGRUWeights(&gradient, 5)
			step++
			applyGRUAdam(&network.weights, &optimizer, gradient, step, config.LearningRate)
		}
	}
	if !validGRUWeights(network.weights, network.inputs, network.hidden) {
		return gruNetwork{}, fmt.Errorf("gru training produced non-finite weights")
	}
	return network, nil
}

type gruState struct {
	update, reset, candidate, hidden []float64
}

func (n gruNetwork) forward(inputs [][]float64) (float64, []gruState) {
	previous := make([]float64, n.hidden)
	states := make([]gruState, len(inputs))
	for t, input := range inputs {
		state := gruState{update: make([]float64, n.hidden), reset: make([]float64, n.hidden), candidate: make([]float64, n.hidden), hidden: make([]float64, n.hidden)}
		for row := 0; row < n.hidden; row++ {
			state.update[row] = sigmoid(gruAffine(n.weights.UpdateInput, n.weights.UpdateHidden, n.weights.UpdateBias[row], input, previous, row, n.inputs, n.hidden))
			state.reset[row] = sigmoid(gruAffine(n.weights.ResetInput, n.weights.ResetHidden, n.weights.ResetBias[row], input, previous, row, n.inputs, n.hidden))
		}
		for row := 0; row < n.hidden; row++ {
			value := n.weights.StateBias[row]
			for column := 0; column < n.inputs; column++ {
				value += n.weights.StateInput[row*n.inputs+column] * input[column]
			}
			for column := 0; column < n.hidden; column++ {
				value += n.weights.StateHidden[row*n.hidden+column] * state.reset[column] * previous[column]
			}
			state.candidate[row] = math.Tanh(value)
			state.hidden[row] = (1-state.update[row])*previous[row] + state.update[row]*state.candidate[row]
		}
		states[t] = state
		previous = state.hidden
	}
	output := n.weights.OutputBias
	for i := range previous {
		output += n.weights.Output[i] * previous[i]
	}
	return output, states
}

func gruAffine(inputWeights, hiddenWeights []float64, bias float64, input, hidden []float64, row, inputSize, hiddenSize int) float64 {
	value := bias
	for column := 0; column < inputSize; column++ {
		value += inputWeights[row*inputSize+column] * input[column]
	}
	for column := 0; column < hiddenSize; column++ {
		value += hiddenWeights[row*hiddenSize+column] * hidden[column]
	}
	return value
}

func (n gruNetwork) backward(inputs [][]float64, states []gruState, outputGradient float64, gradient *gruWeights) {
	last := states[len(states)-1].hidden
	gradient.OutputBias += outputGradient
	dHidden := make([]float64, n.hidden)
	for index := range dHidden {
		gradient.Output[index] += outputGradient * last[index]
		dHidden[index] = outputGradient * n.weights.Output[index]
	}
	for t := len(states) - 1; t >= 0; t-- {
		state := states[t]
		previous := make([]float64, n.hidden)
		if t > 0 {
			previous = states[t-1].hidden
		}
		dPrevious := make([]float64, n.hidden)
		dUpdate, dCandidate := make([]float64, n.hidden), make([]float64, n.hidden)
		for row := 0; row < n.hidden; row++ {
			dPrevious[row] += dHidden[row] * (1 - state.update[row])
			dUpdate[row] = dHidden[row] * (state.candidate[row] - previous[row])
			dCandidate[row] = dHidden[row] * state.update[row]
		}
		dResetProduct := make([]float64, n.hidden)
		for row := 0; row < n.hidden; row++ {
			raw := dCandidate[row] * (1 - state.candidate[row]*state.candidate[row])
			gradient.StateBias[row] += raw
			for column := 0; column < n.inputs; column++ {
				gradient.StateInput[row*n.inputs+column] += raw * inputs[t][column]
			}
			for column := 0; column < n.hidden; column++ {
				gradient.StateHidden[row*n.hidden+column] += raw * state.reset[column] * previous[column]
				dResetProduct[column] += raw * n.weights.StateHidden[row*n.hidden+column]
			}
		}
		dReset := make([]float64, n.hidden)
		for row := 0; row < n.hidden; row++ {
			dReset[row] = dResetProduct[row] * previous[row]
			dPrevious[row] += dResetProduct[row] * state.reset[row]
		}
		for row := 0; row < n.hidden; row++ {
			rawUpdate := dUpdate[row] * state.update[row] * (1 - state.update[row])
			rawReset := dReset[row] * state.reset[row] * (1 - state.reset[row])
			gradient.UpdateBias[row] += rawUpdate
			gradient.ResetBias[row] += rawReset
			for column := 0; column < n.inputs; column++ {
				gradient.UpdateInput[row*n.inputs+column] += rawUpdate * inputs[t][column]
				gradient.ResetInput[row*n.inputs+column] += rawReset * inputs[t][column]
			}
			for column := 0; column < n.hidden; column++ {
				gradient.UpdateHidden[row*n.hidden+column] += rawUpdate * previous[column]
				gradient.ResetHidden[row*n.hidden+column] += rawReset * previous[column]
				dPrevious[column] += rawUpdate*n.weights.UpdateHidden[row*n.hidden+column] + rawReset*n.weights.ResetHidden[row*n.hidden+column]
			}
		}
		dHidden = dPrevious
	}
}

type gruAdam struct {
	first, second                     gruWeights
	outputBiasFirst, outputBiasSecond float64
}

func newGRUAdam(inputs, hidden int) gruAdam {
	return gruAdam{first: zeroGRUWeights(inputs, hidden), second: zeroGRUWeights(inputs, hidden)}
}

func applyGRUAdam(weights *gruWeights, optimizer *gruAdam, gradient gruWeights, step int, learningRate float64) {
	const beta1, beta2, epsilon = .9, .999, 1e-8
	weightSlices := gruWeightSlices(weights)
	gradientSlices := gruWeightSlices(&gradient)
	firstSlices := gruWeightSlices(&optimizer.first)
	secondSlices := gruWeightSlices(&optimizer.second)
	correction1, correction2 := 1-math.Pow(beta1, float64(step)), 1-math.Pow(beta2, float64(step))
	for group := range weightSlices {
		for i := range weightSlices[group] {
			firstSlices[group][i] = beta1*firstSlices[group][i] + (1-beta1)*gradientSlices[group][i]
			secondSlices[group][i] = beta2*secondSlices[group][i] + (1-beta2)*gradientSlices[group][i]*gradientSlices[group][i]
			weightSlices[group][i] -= learningRate * (firstSlices[group][i] / correction1) / (math.Sqrt(secondSlices[group][i]/correction2) + epsilon)
		}
	}
	optimizer.outputBiasFirst = beta1*optimizer.outputBiasFirst + (1-beta1)*gradient.OutputBias
	optimizer.outputBiasSecond = beta2*optimizer.outputBiasSecond + (1-beta2)*gradient.OutputBias*gradient.OutputBias
	weights.OutputBias -= learningRate * (optimizer.outputBiasFirst / correction1) / (math.Sqrt(optimizer.outputBiasSecond/correction2) + epsilon)
}

func newGRUWeights(inputs, hidden int, seed uint64) gruWeights {
	weights := zeroGRUWeights(inputs, hidden)
	limit := 1 / math.Sqrt(float64(inputs+hidden))
	for _, group := range gruWeightSlices(&weights) {
		for index := range group {
			seed = seed*6364136223846793005 + 1442695040888963407
			group[index] = (float64(seed>>11)/float64(uint64(1)<<53)*2 - 1) * limit
		}
	}
	return weights
}

func zeroGRUWeights(inputs, hidden int) gruWeights {
	return gruWeights{
		UpdateInput: make([]float64, hidden*inputs), UpdateHidden: make([]float64, hidden*hidden), UpdateBias: make([]float64, hidden),
		ResetInput: make([]float64, hidden*inputs), ResetHidden: make([]float64, hidden*hidden), ResetBias: make([]float64, hidden),
		StateInput: make([]float64, hidden*inputs), StateHidden: make([]float64, hidden*hidden), StateBias: make([]float64, hidden), Output: make([]float64, hidden),
	}
}

func gruWeightSlices(weights *gruWeights) [][]float64 {
	return [][]float64{weights.UpdateInput, weights.UpdateHidden, weights.UpdateBias, weights.ResetInput, weights.ResetHidden, weights.ResetBias, weights.StateInput, weights.StateHidden, weights.StateBias, weights.Output}
}

func scaleGRUWeights(weights *gruWeights, factor float64) {
	for _, group := range gruWeightSlices(weights) {
		for i := range group {
			group[i] *= factor
		}
	}
	weights.OutputBias *= factor
}

func clipGRUWeights(weights *gruWeights, bound float64) {
	for _, group := range gruWeightSlices(weights) {
		for i := range group {
			if group[i] > bound {
				group[i] = bound
			} else if group[i] < -bound {
				group[i] = -bound
			}
		}
	}
	if weights.OutputBias > bound {
		weights.OutputBias = bound
	} else if weights.OutputBias < -bound {
		weights.OutputBias = -bound
	}
}

func validGRUWeights(weights gruWeights, inputs, hidden int) bool {
	if len(weights.UpdateInput) != hidden*inputs || len(weights.UpdateHidden) != hidden*hidden || len(weights.UpdateBias) != hidden || len(weights.ResetInput) != hidden*inputs || len(weights.ResetHidden) != hidden*hidden || len(weights.ResetBias) != hidden || len(weights.StateInput) != hidden*inputs || len(weights.StateHidden) != hidden*hidden || len(weights.StateBias) != hidden || len(weights.Output) != hidden || !finiteGRU(weights.OutputBias) {
		return false
	}
	for _, group := range gruWeightSlices(&weights) {
		for _, value := range group {
			if !finiteGRU(value) {
				return false
			}
		}
	}
	return true
}

func sigmoid(value float64) float64 {
	if value >= 0 {
		return 1 / (1 + math.Exp(-value))
	}
	exp := math.Exp(value)
	return exp / (1 + exp)
}

func finiteGRU(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
