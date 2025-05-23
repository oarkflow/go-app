package spellcheck

import (
	"bufio"
	"os"
)

var Dict map[string]struct{}

func init() {
	Dict = make(map[string]struct{})
	f, err := os.Open("data/dict.txt")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		Dict[scan.Text()] = struct{}{}
	}
}

// Damerau-Levenshtein distance between a and b.
func Distance(a, b string) int {
	da := []rune(a)
	db := []rune(b)
	n, m := len(da), len(db)
	max := n + m
	d := make([][]int, n+2)
	for i := range d {
		d[i] = make([]int, m+2)
		for j := range d[i] {
			d[i][j] = max
		}
	}
	d[0][0] = max
	for i := 0; i <= n; i++ {
		d[i+1][1] = i
		d[i+1][0] = max
	}
	for j := 0; j <= m; j++ {
		d[1][j+1] = j
		d[0][j+1] = max
	}
	last := make(map[rune]int)
	for i := 1; i <= n; i++ {
		dbIdx := 0
		for j := 1; j <= m; j++ {
			i1 := last[db[j-1]]
			j1 := dbIdx
			cost := 1
			if da[i-1] == db[j-1] {
				cost = 0
				dbIdx = j
			}
			d[i+1][j+1] = min(
				d[i][j]+cost,
				d[i+1][j]+1,
				d[i][j+1]+1,
				d[i1][j1]+(i-i1-1)+1+(j-j1-1),
			)
		}
		last[da[i-1]] = i
	}
	return d[n+1][m+1]
}

func min(a, b, c, d int) int {
	m := a
	for _, x := range []int{b, c, d} {
		if x < m {
			m = x
		}
	}
	return m
}

// Suggest finds the dictionary word with minimal distance.
func Suggest(word string) string {
	best, bestD := word, len(word)+1
	for w := range Dict {
		if d := Distance(word, w); d < bestD {
			bestD, best = d, w
		}
	}
	return best
}
