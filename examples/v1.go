package main

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"time"
)

// =======================
// Configuration Parameters
// =======================

// Data acquisition simulation parameters
const (
	samplingRate       = 50 // Samples per second
	recordDuration     = 15 // Seconds of data for one session
	baseHeartRate      = 70 // Baseline heart rate in beats per minute (BPM)
	irregularityChance = 15 // Percent chance per sample to simulate an abnormal beat
)

// Filtering parameters for adaptive filtering
const (
	baseWindow     = 5     // Minimum window for moving average filter
	maxWindow      = 10    // Maximum window size when variance is high
	varianceThresh = 0.005 // Threshold variance to trigger increased smoothing
)

// Peak detection parameters
const (
	peakThreshold      = 0.6 // Normalized amplitude threshold for a peak
	minPeakDistanceSec = 0.5 // Minimum distance (in seconds) between consecutive peaks
)

// HRV/Anomaly detection parameter
const (
	irregularityStdMs = 80 // RR interval standard deviation threshold in ms
)

// =======================
// Hardware Interface (Simulated)
// =======================

// PPGSensor defines an interface for a PPG sensor.
type PPGSensor interface {
	Connect() error
	ReadData() ([]float64, error)
}

// SimulatedPPGSensor implements a simulated sensor.
type SimulatedPPGSensor struct{}

// Connect simulates connecting to the sensor.
func (s *SimulatedPPGSensor) Connect() error {
	// In real hardware, initialize camera or sensor communications.
	log.Println("Simulated sensor connected.")
	return nil
}

// ReadData simulates reading a PPG signal over a fixed duration.
func (s *SimulatedPPGSensor) ReadData() ([]float64, error) {
	totalSamples := samplingRate * recordDuration
	signal := make([]float64, totalSamples)
	period := float64(60) / baseHeartRate

	for i := 0; i < totalSamples; i++ {
		t := float64(i) / samplingRate
		// Create a baseline sine wave (normalized to [0,1])
		value := 0.5*math.Sin(2*math.Pi*t/period) + 0.5
		// Add random noise
		noise := (rand.Float64() - 0.5) * 0.12
		signal[i] = value + noise

		// Randomly inject an abnormal beat (simulate early/late beat)
		if rand.Float64()*100 < irregularityChance && i > samplingRate && i < totalSamples-samplingRate {
			signal[i] = value * 1.3
		}
	}
	return signal, nil
}

// =======================
// Advanced Signal Processing
// =======================

// adaptiveFilter applies an adaptive moving average filter that adjusts its window
// based on local variance. When variance is high, a larger window (more smoothing)
// is applied.
func adaptiveFilter(input []float64) []float64 {
	n := len(input)
	output := make([]float64, n)

	// For each index, compute local variance using a base window,
	// then select a window size accordingly.
	for i := 0; i < n; i++ {
		// Determine window centered at i (using baseWindow for variance estimate)
		start := max(0, i-baseWindow/2)
		end := min(n, i+baseWindow/2+1)
		localData := input[start:end]
		mean := avg(localData)
		var sumSq float64
		for _, v := range localData {
			diff := v - mean
			sumSq += diff * diff
		}
		localVariance := sumSq / float64(len(localData))

		// Choose window size based on variance
		win := baseWindow
		if localVariance > varianceThresh {
			win = maxWindow
		}

		// Apply moving average with chosen window size
		wStart := max(0, i-win/2)
		wEnd := min(n, i+win/2+1)
		sum := 0.0
		count := 0
		for j := wStart; j < wEnd; j++ {
			sum += input[j]
			count++
		}
		output[i] = sum / float64(count)
	}
	return output
}

// robustPeakDetection implements a peak detection algorithm using derivative analysis.
// It detects a peak when the derivative changes from positive to negative,
// the amplitude exceeds a threshold, and peaks are sufficiently spaced.
func robustPeakDetection(signal []float64) []int {
	var peaks []int
	minDistSamples := int(minPeakDistanceSec * float64(samplingRate))
	lastPeakIdx := -minDistSamples

	// Loop from 1 to len(signal)-1 to check for zero-crossing in derivative.
	for i := 1; i < len(signal)-1; i++ {
		if i-lastPeakIdx < minDistSamples {
			continue
		}
		// Compute approximate derivative around i
		derivPrev := signal[i] - signal[i-1]
		derivNext := signal[i+1] - signal[i]

		// Check for a change from positive to negative derivative (local maximum)
		if derivPrev > 0 && derivNext < 0 && signal[i] > peakThreshold {
			peaks = append(peaks, i)
			lastPeakIdx = i
		}
	}
	return peaks
}

// multiScaleAnalysis applies robust peak detection on two filtered versions of the signal
// (one with the adaptive filter and one with a fixed, smaller window) and then merges
// the detected peaks to reduce false detections.
func multiScaleAnalysis(signal []float64) []int {
	// First scale: use adaptive filtering
	adaptiveFiltered := adaptiveFilter(signal)
	peaksAdaptive := robustPeakDetection(adaptiveFiltered)

	// Second scale: apply a fixed small window moving average (baseWindow)
	fixedFiltered := movingAverageFilter(signal, baseWindow)
	peaksFixed := robustPeakDetection(fixedFiltered)

	// Merge peaks: simplest approach is to take the union (removing duplicates near each other)
	merged := mergePeaks(peaksAdaptive, peaksFixed, int(minPeakDistanceSec*float64(samplingRate)))
	return merged
}

// movingAverageFilter applies a simple moving average filter with a fixed window.
func movingAverageFilter(input []float64, window int) []float64 {
	n := len(input)
	if window < 2 {
		return input
	}
	output := make([]float64, n)
	halfWin := window / 2

	for i := 0; i < n; i++ {
		sum := 0.0
		count := 0
		for j := max(0, i-halfWin); j < min(n, i+halfWin+1); j++ {
			sum += input[j]
			count++
		}
		output[i] = sum / float64(count)
	}
	return output
}

// mergePeaks merges two slices of peak indices while ensuring that peaks within
// a given minimum distance are considered duplicates.
func mergePeaks(a, b []int, minDistance int) []int {
	mergedMap := make(map[int]bool)
	for _, p := range a {
		mergedMap[p] = true
	}
	for _, p := range b {
		// Check if p is far enough from any already added peak.
		duplicate := false
		for existing := range mergedMap {
			if absInt(p-existing) < minDistance {
				duplicate = true
				break
			}
		}
		if !duplicate {
			mergedMap[p] = true
		}
	}
	// Convert map keys to a sorted slice.
	merged := []int{}
	for p := range mergedMap {
		merged = append(merged, p)
	}
	// Simple sort (bubble sort for small slice)
	for i := 0; i < len(merged); i++ {
		for j := i + 1; j < len(merged); j++ {
			if merged[j] < merged[i] {
				merged[i], merged[j] = merged[j], merged[i]
			}
		}
	}
	return merged
}

// =======================
// Feature Extraction and HRV Calculation
// =======================

// computeRRIntervals computes the intervals (in milliseconds) between consecutive peaks.
func computeRRIntervals(peaks []int) []float64 {
	intervals := []float64{}
	for i := 1; i < len(peaks); i++ {
		intervalSec := float64(peaks[i]-peaks[i-1]) / float64(samplingRate)
		intervals = append(intervals, intervalSec*1000) // convert to milliseconds
	}
	return intervals
}

// calculateStdDev computes the standard deviation of a slice of float64 numbers.
func calculateStdDev(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	m := avg(data)
	var sqDiff float64
	for _, v := range data {
		diff := v - m
		sqDiff += diff * diff
	}
	return math.Sqrt(sqDiff / float64(len(data)))
}

// =======================
// Utility functions
// =======================

func avg(data []float64) float64 {
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func absInt(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// =======================
// Classification (Stub / Simple Model)
// =======================

// classifyRhythm returns true if the standard deviation of RR intervals exceeds a threshold,
// indicating a potential irregularity.
func classifyRhythm(rrIntervals []float64) bool {
	stdDev := calculateStdDev(rrIntervals)
	return stdDev > irregularityStdMs
}

// =======================
// Main Processing Pipeline
// =======================

func runPipeline() {
	log.Println("Starting hardware interfacing...")

	// Set up (simulated) sensor
	var sensor PPGSensor = &SimulatedPPGSensor{}
	if err := sensor.Connect(); err != nil {
		log.Fatalf("Sensor connection failed: %v", err)
	}

	// Read data from sensor
	rawSignal, err := sensor.ReadData()
	if err != nil {
		log.Fatalf("Failed to read sensor data: %v", err)
	}
	log.Println("Data acquisition complete.")

	// Advanced signal processing:
	// 1. Adaptive filtering (robust noise reduction)
	adaptiveFiltered := adaptiveFilter(rawSignal)

	// 2. Multi-scale analysis for robust peak detection:
	peaks := multiScaleAnalysis(adaptiveFiltered)
	if len(peaks) < 2 {
		log.Println("Insufficient peaks detected for HRV analysis.")
		return
	}

	// 3. Feature extraction: compute RR intervals
	rrIntervals := computeRRIntervals(peaks)
	avgInterval := avg(rrIntervals)
	calculatedHR := 60000 / avgInterval // BPM calculation
	stdDev := calculateStdDev(rrIntervals)

	// 4. Classification: flag irregular rhythm if HRV (std dev) is too high
	irregular := classifyRhythm(rrIntervals)

	// Output results
	fmt.Println("------- MVP Cardiac Monitoring -------")
	fmt.Printf("Number of detected beats: %d\n", len(peaks))
	fmt.Printf("Average RR interval: %.2f ms\n", avgInterval)
	fmt.Printf("Calculated Heart Rate: %.1f BPM\n", calculatedHR)
	fmt.Printf("RR Interval Standard Deviation: %.2f ms\n", stdDev)
	if irregular {
		fmt.Println("ALERT: Cardiac rhythm irregularity detected!")
	} else {
		fmt.Println("Cardiac rhythm appears regular.")
	}
}

func main() {
	rand.Seed(time.Now().UnixNano())
	runPipeline()
}
