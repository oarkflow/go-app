package main

import (
	"encoding/gob"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	bloomSize = 1 << 16
	k1        = 1.5
	b         = 0.75
)

type Document struct {
	Key       string
	Value     []byte
	Timestamp int64
}

type BloomFilter struct {
	size uint
	bits []bool
	lock sync.Mutex
}

func NewBloomFilter(size uint) *BloomFilter {
	return &BloomFilter{
		size: size,
		bits: make([]bool, size),
	}
}

func (bf *BloomFilter) hash(item string, seed uint32) uint {
	h := fnv.New32a()
	h.Write([]byte(item))
	sum := h.Sum32() ^ seed
	return uint(sum) % bf.size
}

func (bf *BloomFilter) Add(item string) {
	bf.lock.Lock()
	defer bf.lock.Unlock()
	bf.bits[bf.hash(item, 0xA3B1)] = true
	bf.bits[bf.hash(item, 0xF1C2)] = true
}

func (bf *BloomFilter) Test(item string) bool {
	bf.lock.Lock()
	defer bf.lock.Unlock()
	return bf.bits[bf.hash(item, 0xA3B1)] && bf.bits[bf.hash(item, 0xF1C2)]
}

type KVStore struct {
	dir             string
	memtable        map[string]Document
	memtableLock    sync.RWMutex
	walFile         *os.File
	walLock         sync.Mutex
	valueLogFile    *os.File
	valueLogLock    sync.Mutex
	stopCompaction  chan struct{}
	compactionWG    sync.WaitGroup
	arenaPool       sync.Pool
	bloom           *BloomFilter
	writeCounter    int64
	writeCounterMux sync.Mutex
}

func NewKVStore(dir string) (*KVStore, error) {
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		return nil, err
	}
	walPath := filepath.Join(dir, "wal.log")
	walFile, err := os.OpenFile(walPath, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	valuelogPath := filepath.Join(dir, "valuelog.dat")
	valLog, err := os.OpenFile(valuelogPath, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	store := &KVStore{
		dir:            dir,
		memtable:       make(map[string]Document),
		walFile:        walFile,
		valueLogFile:   valLog,
		stopCompaction: make(chan struct{}),
		bloom:          NewBloomFilter(bloomSize),
		arenaPool: sync.Pool{
			New: func() interface{} {
				return make([]byte, 4096)
			},
		},
	}
	store.loadWAL()
	store.compactionWG.Add(1)
	go store.compactionLoop()
	return store, nil
}

func (kv *KVStore) Close() error {
	close(kv.stopCompaction)
	kv.compactionWG.Wait()
	if err := kv.walFile.Close(); err != nil {
		return err
	}
	if err := kv.valueLogFile.Close(); err != nil {
		return err
	}
	return nil
}

func (kv *KVStore) loadWAL() {
	kv.walLock.Lock()
	defer kv.walLock.Unlock()
	_, err := kv.walFile.Seek(0, io.SeekStart)
	if err != nil {
		fmt.Println("Error seeking WAL:", err)
		return
	}
	dec := gob.NewDecoder(kv.walFile)
	for {
		var doc Document
		err := dec.Decode(&doc)
		if err != nil {
			if err == io.EOF {
				break
			}
			fmt.Println("Error decoding WAL:", err)
			break
		}
		kv.memtableLock.Lock()
		if doc.Value == nil {
			delete(kv.memtable, doc.Key)
		} else {
			kv.memtable[doc.Key] = doc
			kv.bloom.Add(doc.Key)
		}
		kv.memtableLock.Unlock()
	}
	_, err = kv.walFile.Seek(0, io.SeekEnd)
	if err != nil {
		fmt.Println("Error seeking WAL to end:", err)
	}
}

func (kv *KVStore) writeWAL(doc Document) error {
	kv.walLock.Lock()
	defer kv.walLock.Unlock()
	enc := gob.NewEncoder(kv.walFile)
	err := enc.Encode(doc)
	if err != nil {
		return err
	}
	kv.writeCounterMux.Lock()
	kv.writeCounter++
	counter := kv.writeCounter
	kv.writeCounterMux.Unlock()
	if counter%10 == 0 {
		if err := kv.walFile.Sync(); err != nil {
			return err
		}
	}
	return nil
}

func (kv *KVStore) writeValueLog(doc Document) error {
	kv.valueLogLock.Lock()
	defer kv.valueLogLock.Unlock()
	enc := gob.NewEncoder(kv.valueLogFile)
	err := enc.Encode(doc)
	if err != nil {
		return err
	}
	return nil
}

func (kv *KVStore) AddDocument(key string, value []byte) error {
	doc := Document{
		Key:       key,
		Value:     value,
		Timestamp: time.Now().UnixNano(),
	}

	if err := kv.writeWAL(doc); err != nil {
		return err
	}
	if err := kv.writeValueLog(doc); err != nil {
		return err
	}
	kv.memtableLock.Lock()
	kv.memtable[key] = doc
	kv.bloom.Add(key)
	kv.memtableLock.Unlock()
	return nil
}

func (kv *KVStore) UpdateDocument(key string, value []byte) error {
	return kv.AddDocument(key, value)
}

func (kv *KVStore) DeleteDocument(key string) error {
	doc := Document{
		Key:       key,
		Value:     nil,
		Timestamp: time.Now().UnixNano(),
	}
	if err := kv.writeWAL(doc); err != nil {
		return err
	}
	if err := kv.writeValueLog(doc); err != nil {
		return err
	}
	kv.memtableLock.Lock()
	delete(kv.memtable, key)
	kv.memtableLock.Unlock()
	return nil
}

func (kv *KVStore) AddDocuments(docs []Document) error {
	kv.walLock.Lock()
	defer kv.walLock.Unlock()
	enc := gob.NewEncoder(kv.walFile)
	for _, doc := range docs {
		doc.Timestamp = time.Now().UnixNano()
		if err := enc.Encode(doc); err != nil {
			return err
		}
		if err := kv.writeValueLog(doc); err != nil {
			return err
		}
		kv.memtableLock.Lock()
		kv.memtable[doc.Key] = doc
		kv.bloom.Add(doc.Key)
		kv.memtableLock.Unlock()
		kv.writeCounterMux.Lock()
		kv.writeCounter++
		kv.writeCounterMux.Unlock()
	}
	return kv.walFile.Sync()
}

func (kv *KVStore) FuzzySearch(query string) []Document {
	kv.memtableLock.RLock()
	defer kv.memtableLock.RUnlock()
	var results []Document
	lQuery := strings.ToLower(query)
	for key, doc := range kv.memtable {
		if !kv.bloom.Test(key) {
			continue
		}
		if strings.Contains(strings.ToLower(key), lQuery) || strings.Contains(strings.ToLower(string(doc.Value)), lQuery) {
			results = append(results, doc)
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Key < results[j].Key
	})
	return results
}

func (kv *KVStore) Paginate(docs []Document, page, pageSize int) []Document {
	start := page * pageSize
	if start >= len(docs) {
		return []Document{}
	}
	end := start + pageSize
	if end > len(docs) {
		end = len(docs)
	}
	return docs[start:end]
}

func BM25Score(doc Document, query string) float64 {
	freq := float64(strings.Count(strings.ToLower(string(doc.Value)), strings.ToLower(query)))
	score := (freq * (k1 + 1)) / (freq + k1)
	return score
}

func (kv *KVStore) QueryDocuments(query string, queryType string) []Document {
	if queryType == "prefix" {
		kv.memtableLock.RLock()
		var results []Document
		for key, doc := range kv.memtable {
			if strings.HasPrefix(key, query) {
				results = append(results, doc)
			}
		}
		kv.memtableLock.RUnlock()
		sort.Slice(results, func(i, j int) bool {
			return results[i].Key < results[j].Key
		})
		return results
	} else if queryType == "fuzzy" {
		return kv.FuzzySearch(query)
	}
	return nil
}

func (kv *KVStore) compactionLoop() {
	defer kv.compactionWG.Done()
	interval := 10 * time.Second
	ticker := time.NewTicker(interval)
	for {
		select {
		case <-ticker.C:
			kv.compactMemtable()
			kv.writeCounterMux.Lock()
			count := kv.writeCounter
			kv.writeCounter = 0
			kv.writeCounterMux.Unlock()
			if count > 100 {
				interval = 5 * time.Second
			} else {
				interval = 10 * time.Second
			}
			ticker.Reset(interval)
		case <-kv.stopCompaction:
			ticker.Stop()
			return
		}
	}
}

func (kv *KVStore) compactMemtable() {
	kv.memtableLock.RLock()
	docs := make([]Document, 0, len(kv.memtable))
	for _, doc := range kv.memtable {
		docs = append(docs, doc)
	}
	kv.memtableLock.RUnlock()
	if len(docs) == 0 {
		return
	}
	sort.Slice(docs, func(i, j int) bool {
		return docs[i].Key < docs[j].Key
	})
	segmentName := fmt.Sprintf("segment_%d.dat", time.Now().UnixNano())
	segmentPath := filepath.Join(kv.dir, segmentName)
	f, err := os.Create(segmentPath)
	if err != nil {
		fmt.Println("Error creating compaction file:", err)
		return
	}
	enc := gob.NewEncoder(f)
	for _, doc := range docs {
		if err := enc.Encode(doc); err != nil {
			fmt.Println("Error writing to compaction file:", err)
			f.Close()
			return
		}
	}
	f.Sync()
	f.Close()
}

func mmapFile(filename string) ([]byte, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() == 0 {
		return nil, fmt.Errorf("empty file")
	}
	b, err := syscall.Mmap(int(f.Fd()), 0, int(stat.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func main() {
	store, err := NewKVStore("data")
	if err != nil {
		fmt.Println("Error initializing KVStore:", err)
		return
	}
	defer store.Close()
	if err := store.AddDocument("doc1", []byte("Hello, world!")); err != nil {
		fmt.Println("Error adding document:", err)
	}
	if err := store.AddDocument("doc2", []byte("Goodbye, world!")); err != nil {
		fmt.Println("Error adding document:", err)
	}
	if err := store.AddDocument("prefix_doc3", []byte("Fuzzy search example")); err != nil {
		fmt.Println("Error adding document:", err)
	}

	batch := []Document{
		{Key: "doc4", Value: []byte("Batch document one")},
		{Key: "doc5", Value: []byte("Batch document two")},
	}
	if err := store.AddDocuments(batch); err != nil {
		fmt.Println("Error in batch add:", err)
	}

	results := store.FuzzySearch("example")
	fmt.Println("Fuzzy Search Results:")
	for _, d := range results {
		score := BM25Score(d, "example")
		fmt.Printf("Key: %s, Score: %f, Value: %s\n", d.Key, score, string(d.Value))
	}
	paged := store.Paginate(results, 0, 2)
	fmt.Println("Paginated Results:")
	for _, d := range paged {
		fmt.Printf("Key: %s, Value: %s\n", d.Key, string(d.Value))
	}
	segmentFiles, _ := filepath.Glob(filepath.Join("data", "segment_*.dat"))
	if len(segmentFiles) > 0 {
		mapped, err := mmapFile(segmentFiles[0])
		if err == nil {
			fmt.Printf("Mapped %d bytes from %s\n", len(mapped), segmentFiles[0])
		} else {
			fmt.Println("Error memory-mapping file:", err)
		}
	}
	prefixResults := store.QueryDocuments("doc", "prefix")
	fmt.Println("QueryDocuments (prefix) Results:")
	for _, d := range prefixResults {
		fmt.Printf("Key: %s, Value: %s\n", d.Key, string(d.Value))
	}
}
