// +build !windows

package cache

import (
	"bytes"
	"encoding/base32"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path"
	"syscall"
	"sync"
)


/* Storage */
type Storage interface {
	Setup() error
	NewContent(string) (StorageContent, error)
}

type StorageContent interface {
	io.WriteCloser
	GetReader() io.Reader
	Clear() error
}

/* Memory Storage */

type MemoryStorage struct{}

type DataContainer struct {
	data []byte
}

type MemoryData struct {
	content         *DataContainer

	subscribers     []*PartiallyContentReader
	subscribersLock *sync.RWMutex

	isClosed        bool
	isClosedLock    *sync.RWMutex
}

type PartiallyContentReader struct {
	offset      int
	content     *DataContainer
	moreContent chan struct{}
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{}
}

func (s *MemoryStorage) Setup() error {
	return nil
}

func (s *MemoryStorage) NewContent(key string) (StorageContent, error) {
	return &MemoryData{
		content: 		 &DataContainer{ data: []byte{} },
		subscribersLock: new(sync.RWMutex),
		isClosedLock: 	 new(sync.RWMutex),
	}, nil
}

func (buff *MemoryData) Write(p []byte) (int, error) {
	buff.content.data = append(buff.content.data, p...)

	fmt.Println("New len=", len(buff.content.data))
	for _, subscriber := range buff.subscribers {
		buff.subscribersLock.RLock()
		go func (subscriber *PartiallyContentReader) {
			fmt.Println("Enviando moreContent signal")
			subscriber.moreContent <- struct{}{}
			buff.subscribersLock.RUnlock()
		}(subscriber)
	}

	return len(p), nil
}

func (buff *MemoryData) GetReader() io.Reader {
	buff.isClosedLock.RLock()
	defer buff.isClosedLock.RUnlock()

	if buff.isClosed {
		// Create a new reader from same bytes
		return bytes.NewReader(buff.content.data[0:])
	}

	reader := &PartiallyContentReader{
		moreContent: make(chan struct{}),
		content:   buff.content,
		offset:    0,
	}

	buff.subscribersLock.Lock()
	defer buff.subscribersLock.Unlock()
	buff.subscribers = append(buff.subscribers, reader)

	return reader
}

func (buff *MemoryData) Close() error {
	// TODO should this return an error if it was already closed?
	buff.isClosedLock.Lock()
	buff.isClosed = true
	buff.isClosedLock.Unlock()

	fmt.Println("Close")
	for _, subscriber := range buff.subscribers {
		buff.subscribersLock.Lock()
		go func (subscriber *PartiallyContentReader) {
			close(subscriber.moreContent)
			buff.subscribersLock.Unlock()
		}(subscriber)
	}

	// TODO Remove subscribers
	return nil
}

func (buff *MemoryData) Clear() error {
	return nil
}


func (reader *PartiallyContentReader) Read(p []byte) (int, error) {
	// TODO add mutex
	if reader.offset < len(reader.content.data) {
		n := copy(p, reader.content.data[reader.offset:])
		reader.offset += n
		return n, nil
	} else {
		for range reader.moreContent {
			if len(reader.content.data[reader.offset:]) == 0 {
				continue
			}
			n := copy(p, reader.content.data[reader.offset:])
			reader.offset += n
			return n, nil
		}
		return 0, io.EOF
	}
}

/*
 *
 * MMap Storage
 *
 */

var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

func randSeq(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

type MMapStorage struct {
	path string
}

type MMapContent struct {
	file    *os.File
	mapping []byte
}

func NewMMapStorage(path string) *MMapStorage {
	return &MMapStorage{path: path}
}

func (s *MMapStorage) Setup() error {
	return os.MkdirAll(s.path, 0700)
}

func (s *MMapStorage) NewContent(key string) (StorageContent, error) {
	filename := path.Join(s.path, base32.StdEncoding.EncodeToString([]byte(key))+randSeq(10))
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	return &MMapContent{file: file}, nil
}

func (data *MMapContent) Write(p []byte) (int, error) {
	return data.file.Write(p)
}

func (data *MMapContent) GetReader() io.Reader {
	return bytes.NewReader(data.mapping)
}

func (data *MMapContent) Close() error {
	if err := data.file.Sync(); err != nil {
		return err
	}

	info, err := data.file.Stat()
	if err != nil {
		fmt.Println(err.Error())
		return err
	}
	fd := int(data.file.Fd())
	flags := syscall.PROT_READ | syscall.PROT_WRITE
	mapping, err := syscall.Mmap(fd, 0, int(info.Size()), flags, syscall.MAP_SHARED)
	if err != nil {
		return err
	}
	data.mapping = mapping
	return nil
}

func (s *MMapContent) Clear() error {
	err := syscall.Munmap(s.mapping)
	if err != nil {
		return err
	}
	filePath := s.file.Name()
	err = s.file.Close()
	if err != nil {
		return err
	}
	return os.Remove(filePath)
}
