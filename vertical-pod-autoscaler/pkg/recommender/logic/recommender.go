/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package logic

import (
	"flag"
	"math"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	
	// client-go
	"time"
	"encoding/json"
	"strconv"
	
	"k8s.io/client-go/kubernetes"
	// "k8s.io/klog"
)

const (
	P_NOM = 0.8
	SLA = 1.0 // set point of the system
	A = 0.5 // value from 0 to 1 to change how the control is conservative
	A1_NOM = 0.1963
	A2_NOM = 0.002
	A3_NOM = 0.5658
	// CORE_MIN = 1.0
	CORE_MAX = 1.0
)

var (
	safetyMarginFraction = flag.Float64("recommendation-margin-fraction", 0.15, `Fraction of usage added as the safety margin to the recommended request`)
	podMinCPUMillicores  = flag.Float64("pod-recommendation-min-cpu-millicores", 25, `Minimum CPU recommendation for a pod`)
	podMinMemoryMb       = flag.Float64("pod-recommendation-min-memory-mb", 250, `Minimum memory recommendation for a pod`)

	uiOld = 0.0
	old_count = 0 // store the previous value of the requests counter
)

type MetricValueList struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Metadata   struct {
		SelfLink string `json:"selfLink"`
	} `json:"metadata"`
	Items []struct {
		DescribedObject struct {
			Kind          string    `json:"kind"`
			Namespace     string    `json:"namespace"`
			Name          string    `json:"name"`
			ApiVersion 		string 		`json:"apiVersion"`
		} `json:"describedObject"`
		MetricName  string    `json:"metricName"`
		Timestamp	  time.Time `json:"timestamp"`				
		Value 			string    `json:"value"`
	} `json:"items"`
}

// PodResourceRecommender computes resource recommendation for a Vpa object.
type PodResourceRecommender interface {
	GetRecommendedPodResources(containerNameToAggregateStateMap model.ContainerNameToAggregateStateMap, customClient *kubernetes.Clientset) RecommendedPodResources
}

// RecommendedPodResources is a Map from container name to recommended resources.
type RecommendedPodResources map[string]RecommendedContainerResources

// RecommendedContainerResources is the recommendation of resources for a
// container.
type RecommendedContainerResources struct {
	// Recommended optimal amount of resources.
	Target model.Resources
	// Recommended minimum amount of resources.
	LowerBound model.Resources
	// Recommended maximum amount of resources.
	UpperBound model.Resources
}

type podResourceRecommender struct {
	targetEstimator     ResourceEstimator
	lowerBoundEstimator ResourceEstimator
	upperBoundEstimator ResourceEstimator
}

func (r *podResourceRecommender) GetRecommendedPodResources(containerNameToAggregateStateMap model.ContainerNameToAggregateStateMap, customClient *kubernetes.Clientset) RecommendedPodResources {
	var recommendation = make(RecommendedPodResources)
	if len(containerNameToAggregateStateMap) == 0 {
		return recommendation
	}

	fraction := 1.0 / float64(len(containerNameToAggregateStateMap))
	minResources := model.Resources{
		model.ResourceCPU:    model.ScaleResource(model.CPUAmountFromCores(*podMinCPUMillicores*0.001), fraction),
		model.ResourceMemory: model.ScaleResource(model.MemoryAmountFromBytes(*podMinMemoryMb*1024*1024), fraction),
	}

	recommender := &podResourceRecommender{
		WithMinResources(minResources, r.targetEstimator),
		WithMinResources(minResources, r.lowerBoundEstimator),
		WithMinResources(minResources, r.upperBoundEstimator),
	}

	for containerName, aggregatedContainerState := range containerNameToAggregateStateMap {
		recommendation[containerName] = recommender.estimateContainerResources(aggregatedContainerState, customClient, containerName)
	}
	return recommendation
}

// Takes AggregateContainerState and returns a container recommendation.
func (r *podResourceRecommender) estimateContainerResources(s *model.AggregateContainerState,
	customClient *kubernetes.Clientset, containerName string) RecommendedContainerResources {

	if (containerName == "our-fantastic-app") {
		// custom metrics
		var metrics MetricValueList
		metricName := "nginx_http_requests_per_second"
		err := getMetrics(customClient, &metrics, metricName)
		if err != nil {
			panic(err.Error())
		}
		// TODO: directly fetch the value from the metrics.Items object
		// Try if this works:
		// value := parseValue(metrics.Items[0].value)

		// for _, m := range metrics.Items {
		// 	fmt.Println("Pod:", m.DescribedObject.Name,"\tNamespace:", m.DescribedObject.Namespace,
		// 	"\nMetric Name:", m.MetricName,"\tValue:", value,"\nTimestamp:", m.Timestamp.String())
		// }
		
		// metricName = "nginx_connections_accepted"
		// err = getMetrics(customClient, &metrics, metricName)
		// if err != nil {
		// 	klog.Errorf("Cannot get metric %s from Prometheus. Reason: %+v", metricName, err)
		// }
		// for _, m := range metrics.Items {
		// 	value := parseValue(m.Value)
		// 	fmt.Println("Pod:", m.DescribedObject.Name,"\tNamespace:", m.DescribedObject.Namespace,
		// 	"\nMetric Name:", m.MetricName,"\tValue:", value,"\nTimestamp:", m.Timestamp.String())
		// }

		// TODO: test variables, shall be replaced by real-time values
		requests := 80 - old_count
		old_count = 80 // new count
		respTime := 5.2
	
		req := float64(requests) // active requests + queue of requests
		rt := respTime // mean of the response times
		error := SLA/1000 - rt/1000
		ke := (A-1)/(P_NOM-1)*error
		ui := uiOld+(1-P_NOM)*ke
		ut := ui+ke
	
		targetCore := req*(ut-A1_NOM-1000.0*A2_NOM)/(1000.0*A3_NOM*(A1_NOM-ut))
	
		approxCore := math.Min(math.Max(math.Abs(targetCore), *podMinCPUMillicores/1000.0), CORE_MAX)
		
		approxUt := ((1000.0*A2_NOM+A1_NOM)*req+1000.0*A1_NOM*A3_NOM*approxCore)/(req+1000.0*A3_NOM*approxCore)
		uiOld = approxUt-ke
		
		// TODO: Find the default value of the memory of the deployment file
		// TODO: handle CORE_MAX correctly
		return RecommendedContainerResources{
			Target: model.Resources{
				model.ResourceCPU: model.CPUAmountFromCores(targetCore),
				model.ResourceMemory: r.targetEstimator.GetResourceEstimation(s)["memory"],
			},
			LowerBound: r.lowerBoundEstimator.GetResourceEstimation(s),
			UpperBound: r.upperBoundEstimator.GetResourceEstimation(s),
		}
	} else {
		return RecommendedContainerResources{
			r.targetEstimator.GetResourceEstimation(s),
			r.lowerBoundEstimator.GetResourceEstimation(s),
			r.upperBoundEstimator.GetResourceEstimation(s),
		}
	}
	
}

// CreatePodResourceRecommender returns the primary recommender.
func CreatePodResourceRecommender() PodResourceRecommender {
	targetCPUPercentile := 0.9
	lowerBoundCPUPercentile := 0.5
	upperBoundCPUPercentile := 0.95

	targetMemoryPeaksPercentile := 0.9
	lowerBoundMemoryPeaksPercentile := 0.5
	upperBoundMemoryPeaksPercentile := 0.95

	targetEstimator := NewPercentileEstimator(targetCPUPercentile, targetMemoryPeaksPercentile)
	lowerBoundEstimator := NewPercentileEstimator(lowerBoundCPUPercentile, lowerBoundMemoryPeaksPercentile)
	upperBoundEstimator := NewPercentileEstimator(upperBoundCPUPercentile, upperBoundMemoryPeaksPercentile)

	targetEstimator = WithMargin(*safetyMarginFraction, targetEstimator)
	lowerBoundEstimator = WithMargin(*safetyMarginFraction, lowerBoundEstimator)
	upperBoundEstimator = WithMargin(*safetyMarginFraction, upperBoundEstimator)

	// Apply confidence multiplier to the upper bound estimator. This means
	// that the updater will be less eager to evict pods with short history
	// in order to reclaim unused resources.
	// Using the confidence multiplier 1 with exponent +1 means that
	// the upper bound is multiplied by (1 + 1/history-length-in-days).
	// See estimator.go to see how the history length and the confidence
	// multiplier are determined. The formula yields the following multipliers:
	// No history     : *INF  (do not force pod eviction)
	// 12h history    : *3    (force pod eviction if the request is > 3 * upper bound)
	// 24h history    : *2
	// 1 week history : *1.14
	upperBoundEstimator = WithConfidenceMultiplier(1.0, 1.0, upperBoundEstimator)

	// Apply confidence multiplier to the lower bound estimator. This means
	// that the updater will be less eager to evict pods with short history
	// in order to provision them with more resources.
	// Using the confidence multiplier 0.001 with exponent -2 means that
	// the lower bound is multiplied by the factor (1 + 0.001/history-length-in-days)^-2
	// (which is very rapidly converging to 1.0).
	// See estimator.go to see how the history length and the confidence
	// multiplier are determined. The formula yields the following multipliers:
	// No history   : *0   (do not force pod eviction)
	// 5m history   : *0.6 (force pod eviction if the request is < 0.6 * lower bound)
	// 30m history  : *0.9
	// 60m history  : *0.95
	lowerBoundEstimator = WithConfidenceMultiplier(0.001, -2.0, lowerBoundEstimator)

	return &podResourceRecommender{
		targetEstimator,
		lowerBoundEstimator,
		upperBoundEstimator}
}


func getMetrics(clientset *kubernetes.Clientset, metrics *MetricValueList, metricName string) error {
	data, err := clientset.RESTClient().Get().AbsPath("apis/custom.metrics.k8s.io/v1beta1/namespaces/nginx-ingress/pods/*/"+metricName).DoRaw()
	if err != nil {
		return err
	}
	err = json.Unmarshal(data, &metrics)
	return err
}

func parseValue(value string) (float64) {
	multiplier := 1.0
	if value[len(value)-1] == 'm' {
		multiplier = 0.001
		value = value[:len(value)-1]
	}

	fValue, err := strconv.ParseFloat(value, 32)
	if err != nil {
		return 0
	}
	return fValue * multiplier
}