package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/abiosoft/semaphore"
	"gopkg.in/cheggaaa/pb.v1"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

const edgecastLinkBegin string = "http://"
const edgecastLinkBaseEnd string = "index-dvr.m3u8"
const edgecastLinkM3U8End string = ".m3u8"
const targetdurationStart string = "TARGETDURATION:"
const targetdurationEnd string = "\n#ID3"
const ffmpegCMD string = `ffmpeg`
const group int		= 100

var sem = semaphore.New(5)

/*
	Returns the signature and token from a tokenAPILink
	signature and token are needed for accessing the usher api
*/
func accessTokenAPI(tokenAPILink string) (string, string, error) {
	resp, err := http.Get(tokenAPILink)
	if err != nil {
		return "", "", err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}

	// See https://blog.golang.org/json-and-go "Decoding arbitrary data"
	var data interface{}
	err = json.Unmarshal(body, &data)
	m := data.(map[string]interface{})
	sig := fmt.Sprintf("%v", m["sig"])
	token := fmt.Sprintf("%v", m["token"])
	return sig, token, err
}

func accessUsherAPI(usherAPILink string) (string, string, error) {
	resp, err := http.Get(usherAPILink)
	if err != nil {
		return "", "", err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}

	respString := string(body)

	m3u8Link := respString[strings.Index(respString, edgecastLinkBegin) : strings.Index(respString, edgecastLinkM3U8End)+len(edgecastLinkM3U8End)]
	edgecastBaseURL := respString[strings.Index(respString, edgecastLinkBegin):strings.Index(respString, edgecastLinkBaseEnd)]

	return edgecastBaseURL, m3u8Link, err
}

func getM3U8List(m3u8Link string) (string, error) {
	resp, err := http.Get(m3u8Link)
	if err != nil {
		return "", err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), err
}

/*
	Returns the number of chunks to download based of the start and end time and the target duration of a
	chunk. Adding 1 to overshoot the end by a bit
*/
func numberOfChunks(sh int, sm int, ss int, eh int, em int, es int, target int) int {
	start_seconds := sh*3600 + sm*60 + ss
	end_seconds := eh*3600 + em*60 + es

	return ((end_seconds - start_seconds) / target) + 1
}

func startingChunk(sh int, sm int, ss int, target int) int {
	start_seconds := sh*3600 + sm*60 + ss
	return (start_seconds / target)
}


func file_exists(f string) bool {
	_, err := os.Stat(f)
	if os.IsNotExist(err) {
		return false
	}
	return err == nil
}

func downloadChunk(edgecastBaseURL string, chunkNum int, vodID string) {

	filename := fmt.Sprintf("%s_%04d.mp4",vodID, chunkNum )
	if( file_exists(filename) ) {
		return
	}
	resp, err := http.Get(edgecastBaseURL + strconv.Itoa(chunkNum) + ".ts")
	if err != nil {
		fmt.Println("http.Get Error:"+err.Error())
		os.Exit(1)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("ioutil.ReadAll Error:"+err.Error())
		os.Exit(1)
	}


	_ = ioutil.WriteFile(filename, body, 0644)

}

/*


 */
func ffmpegCombine( input []string, output string) bool {
	concat := `concat:` + strings.Join( input,"|")

	args := []string{"-i", concat, "-c", "copy", "-bsf:a", "aac_adtstoasc", "-fflags", "+genpts", output }

	cmd := exec.Command(ffmpegCMD, args...)
	var errbuf bytes.Buffer
	cmd.Stderr = &errbuf
	err := cmd.Run()
	if err != nil {
		fmt.Println(errbuf.String())
		fmt.Println("ffmpeg error")
		return false
	}

	for _,del := range input {
		os.Remove(del)
	}
	return true
}

/*


 */
func ffmpegCombineChunks(chunkNum int, startChunk int, vodID string) bool {

	var input []string;
	for i := startChunk; i < (startChunk + chunkNum); i++ {
		input = append(input, fmt.Sprintf("%s_%04d.mp4",vodID, i ))
	}

	output := fmt.Sprintf("%s_c%02d.mp4",vodID,startChunk / group)

	if( file_exists( output ) )  {
		for i:= 0; i < 10; i++  {
			output = fmt.Sprintf("%s_c%02d_%02d.mp4",vodID,startChunk / group,i)
			if( !file_exists( output ) ) { break }
		}
	}



	return ffmpegCombine( input, output )


}

func downloadAndCombine(edgecastBaseURL string, chunkNum int, startChunk int, vodID string) {

	var wg sync.WaitGroup
	wg.Add(chunkNum)

	bar := pb.StartNew(chunkNum).Prefix( fmt.Sprintf("Chunk: %4d",startChunk))

	for i := startChunk; i < (startChunk + chunkNum); i++ {

		go func(chunk int) {
			//Only 5 simultaneous downloads
			sem.Acquire()
			downloadChunk(edgecastBaseURL, chunk, vodID)
			bar.Increment()
			defer wg.Done()

			sem.Release()
		}(i)
	}
	wg.Wait()
	bar.Finish()

	fmt.Println("Combining parts...")

	ffmpegCombineChunks(chunkNum, startChunk, vodID)
}


func wrongInputNotification() {
	fmt.Println("Call the program with the vod id, start and end time following: concat.exe VODID HH MM SS HH MM SS\nwhere VODID is the number you see in the url of the vod (https://www.twitch.tv/videos/123456789 => 123456789) the first HH MM SS is the start time and the second HH MM SS is the end time.\nSo downloading the first one and a half hours of a vod would be: concat.exe 123456789 0 0 0 1 30 0")
	os.Exit(1)
}

func main() {
	var vodID, vodSH, vodSM, vodSS, vodEH, vodEM, vodES int
	var vodIDString string
	if len(os.Args) >= 8 {
		vodIDString = os.Args[1]
		vodID, _ = strconv.Atoi(os.Args[1])
		vodSH, _ = strconv.Atoi(os.Args[2]) //start Hour
		vodSM, _ = strconv.Atoi(os.Args[3]) //start minute
		vodSS, _ = strconv.Atoi(os.Args[4]) //start second
		vodEH, _ = strconv.Atoi(os.Args[5]) //end hour
		vodEM, _ = strconv.Atoi(os.Args[6]) //end minute
		vodES, _ = strconv.Atoi(os.Args[7]) //end second
	} else {
		wrongInputNotification()
	}

	if (vodSH*3600 + vodSM*60 + vodSS) > (vodEH*3600 + vodEM*60 + vodES) {
		wrongInputNotification()
	}

	tokenAPILink := fmt.Sprintf("http://api.twitch.tv/api/vods/%v/access_token?&client_id=aokchnui2n8q38g0vezl9hq6htzy4c", vodID)

	fmt.Println("Contacting Twitch Server")

	sig, token, err := accessTokenAPI(tokenAPILink)
	if err != nil {
		fmt.Println("Couldn't access twitch token api:"+err.Error())
		os.Exit(1)
	}

	usherAPILink := fmt.Sprintf("http://usher.twitch.tv/vod/%v?nauthsig=%v&nauth=%v", vodID, sig, token)

	edgecastBaseURL, m3u8Link, err := accessUsherAPI(usherAPILink)
	if err != nil {
		fmt.Println("Couldn't access usher api"+err.Error())
		os.Exit(1)
	}

	fmt.Println("Getting Video info")

	m3u8List, err := getM3U8List(m3u8Link)
	if err != nil {
		fmt.Println("Couldn't download m3u8 list"+err.Error())
		os.Exit(1)
	}

	targetduration, _ := strconv.Atoi(m3u8List[strings.Index(m3u8List, targetdurationStart)+len(targetdurationStart) : strings.Index(m3u8List, targetdurationEnd)])
	chunkNum := numberOfChunks(vodSH, vodSM, vodSS, vodEH, vodEM, vodES, targetduration)
	startChunk := startingChunk(vodSH, vodSM, vodSS, targetduration)

	fmt.Printf("Starting Download:( %4d : %4d )\n",startChunk,chunkNum)

	//All-in-one
	//downloadAndCombine( edgecastBaseURL, chunkNum, startChunk, vodIDString )

	//group-wise
	lastChunk  := startChunk + chunkNum

	startGroup :=	startChunk / group
	 lastGroup := (lastChunk-1) / group + 1

	for i := startGroup; i < lastGroup; i++ {

		//group in multiples of group. First and last may not be full groups
		start := i*group
		if( start < startChunk ){ start = startChunk }

		end := (i+1)*group
		if( end > lastChunk ) { end = lastChunk }

		downloadAndCombine( edgecastBaseURL, end-start, start, vodIDString )
	}

	fmt.Println("All done!")
}
