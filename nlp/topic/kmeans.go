package topic

import (
	"math"
	"math/rand"
)

// Centers are TF-IDF vectors in sparse map form.
type Center map[string]float64

func Cluster(docs []map[string]float64, k, iter int) []Center {
	// Initialize random centers
	centers := make([]Center, k)
	for i := 0; i < k; i++ {
		centers[i] = docs[rand.Intn(len(docs))]
	}
	// Lloyd’s algorithm
	for it := 0; it < iter; it++ {
		clusters := make([][]Center, k)
		// assign
		for _, doc := range docs {
			best, bestDist := 0, math.Inf(1)
			for i, c := range centers {
				d := dist(doc, c)
				if d < bestDist {
					bestDist, best = d, i
				}
			}
			clusters[best] = append(clusters[best], doc)
		}
		// update
		for i := 0; i < k; i++ {
			centers[i] = mean(clusters[i])
		}
	}
	return centers
}

func dist(a, b Center) float64 {
	sum := 0.0
	for w, av := range a {
		bv := b[w]
		sum += (av - bv) * (av - bv)
	}
	return math.Sqrt(sum)
}

func mean(cluster []Center) Center {
	m := make(Center)
	if len(cluster) == 0 {
		return m
	}
	for _, doc := range cluster {
		for w, v := range doc {
			m[w] += v
		}
	}
	for w := range m {
		m[w] /= float64(len(cluster))
	}
	return m
}
