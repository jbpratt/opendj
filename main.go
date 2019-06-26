package opendj

import (
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/youtube/v3"
)

// Dj ...
type Dj struct {
	waitingQueue queue
	currentEntry QueueEntry

	runningCommand *exec.Cmd

	playbackFeed   chan QueueEntry
	playbackErrors chan error

	songStarted time.Time
}

// Video can be anything youtube-dl supports
type Video struct {
	Title    string
	URL      string
	Duration time.Duration
}

// QueueEntry ...
type QueueEntry struct {
	Video      Video
	User       string
	Dedication string
}

type queue struct {
	Items []QueueEntry
	sync.Mutex
}

const youtubeURLStart = "https://www.youtube.com/watch?v="

// NewDj returns a new Dj struct and channels for errors playback events
func NewDj(queue []QueueEntry) (dj *Dj, playbackFeed <-chan QueueEntry, playbackErrorFeed <-chan error, err error) {
	dj = &Dj{}
	dj.waitingQueue.Items = queue
	dj.playbackFeed = make(chan QueueEntry, 100)
	dj.playbackErrors = make(chan error, 100)

	return dj, dj.playbackFeed, dj.playbackErrors, nil
}

// YouTubeVideo returns a video struct for a given youtube ID and any encoutered errors
func (dj *Dj) YouTubeVideo(videoID string, ytServ *youtube.Service) (video Video, err error) {
	res, err := ytServ.Videos.List("id,snippet,contentDetails").Id(videoID).Do()
	if err != nil {
		return video, err
	} else if len(res.Items) < 1 {
		return video, errors.New("Video not found")
	}
	video.Title = res.Items[0].Snippet.Title
	video.URL = youtubeURLStart + res.Items[0].Id
	video.Duration, _ = time.ParseDuration(strings.ToLower(res.Items[0].ContentDetails.Duration[2:]))
	return video, nil
}

// Queue return the current queue as a list of queue entries
func (dj *Dj) Queue() []QueueEntry {
	return dj.waitingQueue.Items
}

// AddEntry adds the passed QueueEntry at the end of the queue
func (dj *Dj) AddEntry(newEntry QueueEntry) {
	dj.waitingQueue.Lock()
	dj.waitingQueue.Items = append(dj.waitingQueue.Items, newEntry)
	dj.waitingQueue.Unlock()
}

// InsertEntry inserts the passed QueueEntry into the queue at the given index
// if the index is too high it has the same effect as AddEntry()
// returns an error if the index is < 0
func (dj *Dj) InsertEntry(newEntry QueueEntry, index int) error {
	dj.waitingQueue.Lock()
	defer dj.waitingQueue.Unlock()

	if index < 0 {
		return errors.New("index out of range")
	} else if index >= len(dj.waitingQueue.Items) {
		dj.waitingQueue.Items = append(dj.waitingQueue.Items, newEntry)
		return nil
	}
	dj.waitingQueue.Items = append(dj.waitingQueue.Items, QueueEntry{})
	copy(dj.waitingQueue.Items[index+1:], dj.waitingQueue.Items[index:])
	dj.waitingQueue.Items[index] = newEntry
	return nil
}

// RemoveIndex removes the element the given index from the queue
// returns an error if the index is out of range
func (dj *Dj) RemoveIndex(index int) error {
	dj.waitingQueue.Lock()
	defer dj.waitingQueue.Unlock()
	if index >= len(dj.waitingQueue.Items) || index < 0 {
		return errors.New("index out of range")
	}
	dj.waitingQueue.Items = append(dj.waitingQueue.Items[:index], dj.waitingQueue.Items[index+1:]...)
	return nil
}

// ChangeIndex swaps the element the given index for the provided one
// returns an error if the index is out of range
func (dj *Dj) ChangeIndex(newEntry QueueEntry, index int) error {
	dj.waitingQueue.Lock()
	defer dj.waitingQueue.Unlock()

	if index < 0 || index >= len(dj.waitingQueue.Items) {
		return errors.New("index out of range")
	}

	dj.waitingQueue.Items[index] = newEntry

	return nil
}

func (dj *Dj) pop() (QueueEntry, error) {
	dj.waitingQueue.Lock()
	defer dj.waitingQueue.Unlock()

	if len(dj.waitingQueue.Items) < 1 {
		return QueueEntry{}, errors.New("can't pop from empty queue")
	}

	entry := dj.waitingQueue.Items[0]
	dj.waitingQueue.Items = dj.waitingQueue.Items[1:]
	return entry, nil
}

// EntryAtIndex returns the QueueEntry at the given index or error if the index is out of range
func (dj *Dj) EntryAtIndex(index int) (QueueEntry, error) {
	dj.waitingQueue.Lock()
	defer dj.waitingQueue.Unlock()

	if index >= len(dj.waitingQueue.Items) || index < 0 {
		return QueueEntry{}, errors.New("index out of range")
	}

	entry := dj.waitingQueue.Items[index]

	return entry, nil
}

// Play starts the playback to the given RTMP server
// if nothing is in the playlist it waits for new content to be added
// any erncountered errors are sent to the playbackErrorFeed
func (dj *Dj) Play(rtmpServer string) {
	for {
		entry, err := dj.pop()
		if err != nil {
			// TODO: not ideal, maybe have a backup playlist
			time.Sleep(time.Second * 5)
			continue
		}
		dj.currentEntry = entry

		command := exec.Command("youtube-dl", "-f", "bestaudio", "-g", dj.currentEntry.Video.URL)
		url, err := command.Output()
		if err != nil {
			dj.playbackErrors <- err
			continue
		}

		urlProper := strings.TrimSpace(string(url))
		dj.songStarted = time.Now()

		command = exec.Command("ffmpeg", "-reconnect", "1", "-reconnect_at_eof", "1", "-reconnect_delay_max", "3", "-re", "-i", urlProper, "-codec:a", "aac", "-f", "flv", rtmpServer)
		err = command.Start()
		if err != nil {
			dj.playbackErrors <- err
			continue
		}
		dj.playbackFeed <- dj.currentEntry

		dj.runningCommand = command

		err = command.Wait()
		if err != nil {
			dj.playbackErrors <- err
		}

		dj.runningCommand = nil
	}
}

// UserPosition returns a slice of all the position in the queue that belong to the given user
func (dj *Dj) UserPosition(nick string) (positions []int) {
	dj.waitingQueue.Lock()
	defer dj.waitingQueue.Unlock()

	for i, content := range dj.waitingQueue.Items {
		if content.User == nick {
			positions = append(positions, i)
		}
	}
	return positions
}

// DurationUntilUser returns a slice of all the durations to the songs in the queue that belong to the given user
func (dj *Dj) DurationUntilUser(nick string) (durations []time.Duration) {
	dj.waitingQueue.Lock()
	defer dj.waitingQueue.Unlock()

	dur := dj.currentEntry.Video.Duration - time.Since(dj.songStarted)
	for _, content := range dj.waitingQueue.Items {
		if content.User == nick {
			durations = append(durations, dur)
		}
		dur += content.Video.Duration
	}
	return durations
}

// ChangeDedication changes the dedication of the queue entry at the given index
// returns an error if the index is out of range
func (dj *Dj) ChangeDedication(index int, dedication string) error {
	dj.waitingQueue.Lock()
	defer dj.waitingQueue.Unlock()

	if index < 0 || index >= len(dj.waitingQueue.Items) {
		return errors.New("index out of range")
	}

	dj.waitingQueue.Items[index].Dedication = dedication
	return nil
}