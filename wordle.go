// Command wordle plays the daily NYT Wordle puzzle in your terminal.
//
//	wordle       today's puzzle
//	wordle -r    random historical puzzle
//	wordle -h    browse past puzzles

package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

//go:embed valid_words.txt
var validWordsRaw string

var (
	wordleLaunch = time.Date(2021, 6, 19, 0, 0, 0, 0, time.UTC)
	httpClient   = &http.Client{Timeout: 10 * time.Second}
	stdinFd      = int(os.Stdin.Fd())

	validWords = func() map[string]bool {
		m := make(map[string]bool)
		for _, w := range strings.Fields(validWordsRaw) {
			m[w] = true
		}
		return m
	}()
)

const (
	reset    = "\033[0m"
	bold     = "\033[1m"
	italic   = "\033[3m"
	hideCur  = "\033[?25l"
	showCur  = "\033[?25h"
	clearScr = "\033[2J\033[H"
	nl       = "\r\n" // raw mode: \n alone moves down but not to column 0
)

const (
	animFlipDelay   = 80 * time.Millisecond
	animRevealDelay = 180 * time.Millisecond
	animShakeDelay  = 50 * time.Millisecond
	animPulseDelay  = 150 * time.Millisecond
	animIntroDelay  = 120 * time.Millisecond
	animIntroPause  = 300 * time.Millisecond
)

const historyCount = 30

func bg(c [3]int, s string) string {
	return fmt.Sprintf("\033[48;2;%d;%d;%dm%s%s", c[0], c[1], c[2], s, reset)
}
func fg(c [3]int, s string) string {
	return fmt.Sprintf("\033[38;2;%d;%d;%dm%s%s", c[0], c[1], c[2], s, reset)
}

// wordle palette (matching NYT)
var (
	colGreen       = [3]int{83, 141, 78}
	colYellow      = [3]int{181, 159, 59}
	colGray        = [3]int{58, 58, 60}
	colEmpty       = [3]int{50, 50, 54}
	colRed         = [3]int{190, 70, 70}
	colWhite       = [3]int{255, 255, 255}
	colGrayTileFg  = [3]int{220, 220, 220}
	colText        = [3]int{215, 218, 220}
	colAccent      = [3]int{120, 200, 255}
	colMuted       = [3]int{128, 128, 132}
	colFlipBg      = [3]int{100, 100, 106}
	colFlipFg      = [3]int{120, 120, 126}
	colKeyDefault  = [3]int{78, 80, 84}
	colKeyAbsent   = [3]int{28, 28, 30}
	colKeyAbsentFg = [3]int{65, 65, 68}
)

type mark int

const (
	markEmpty    mark = iota // no letter yet
	markPending              // letter typed, not yet scored
	markWarn                 // known-absent letter highlighted while typing
	markFlipping             // mid-flip animation frame
	markGray                 // scored: not in word
	markYellow               // scored: wrong position
	markGreen                // scored: correct position
)

var cheers = []string{"genius!", "magnificent", "impressive", "splendid", "great", "phew"}

func tile(letter byte, m mark) string {
	ch := " "
	if letter != 0 {
		ch = strings.ToUpper(string(letter))
	}
	content := " " + ch + " "
	switch m {
	case markGreen:
		return bg(colGreen, fg(colWhite, bold+content))
	case markYellow:
		return bg(colYellow, fg(colWhite, bold+content))
	case markGray:
		return bg(colGray, fg(colGrayTileFg, content))
	case markFlipping:
		return bg(colFlipBg, fg(colFlipFg, " ─ "))
	case markWarn:
		return bg(colEmpty, fg(colRed, bold+content))
	case markPending:
		return bg(colEmpty, fg(colText, bold+content))
	default:
		return bg(colEmpty, fg(colMuted, content))
	}
}

func emojiFor(m mark) string {
	switch m {
	case markGreen:
		return "🟩"
	case markYellow:
		return "🟨"
	default:
		return "⬛"
	}
}

func scoreGuess(guess, solution string) [5]mark {
	var out [5]mark
	remaining := []byte(solution)
	gChars := []byte(guess)

	for pos := 0; pos < 5; pos++ {
		if gChars[pos] == remaining[pos] {
			out[pos] = markGreen
			remaining[pos] = 0
		}
	}
	for pos := 0; pos < 5; pos++ {
		if out[pos] != markGreen {
			out[pos] = markGray
			for solPos := 0; solPos < 5; solPos++ {
				if remaining[solPos] == gChars[pos] {
					out[pos] = markYellow
					remaining[solPos] = 0
					break
				}
			}
		}
	}
	return out
}

func isWin(marks [5]mark) bool {
	for _, m := range marks {
		if m != markGreen {
			return false
		}
	}
	return true
}

type wordleResponse struct {
	Solution        string `json:"solution"`
	DaysSinceLaunch int    `json:"days_since_launch"`
	PrintDate       string `json:"print_date"`
}

func fetchSolution(date string) (*wordleResponse, error) {
	url := fmt.Sprintf("https://www.nytimes.com/svc/wordle/v2/%s.json", date)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var w wordleResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}
	w.Solution = strings.ToLower(w.Solution)
	if w.PrintDate == "" {
		w.PrintDate = date
	}
	return &w, nil
}

// out-of-ASCII sentinels so arrow keys never collide with typed letters
const (
	keyArrowUp   byte = 0xF1
	keyArrowDown byte = 0xF2
)

func readKey() (byte, error) {
	buf := make([]byte, 4)
	n, err := os.Stdin.Read(buf)
	if err != nil {
		return 0, err
	}
	if n >= 3 && buf[0] == 27 && buf[1] == '[' {
		switch buf[2] {
		case 'A':
			return keyArrowUp, nil
		case 'B':
			return keyArrowDown, nil
		}
	}
	if n > 1 && buf[0] == 27 {
		return 0, nil // unrecognised escape sequence
	}
	return buf[0], nil
}

type game struct {
	solution  string
	dayNum    int
	date      string
	offline   bool
	localWord bool // true when the NYT API was unavailable and we fell back locally
	guesses   [6]string
	marks     [6][5]mark
	row       int
	current   string
	message   string
}

func newGame(sol string, day int, date string, offline, localWord bool) *game {
	validWords[sol] = true // answer must always be an accepted guess
	return &game{solution: sol, dayNum: day, date: date, offline: offline, localWord: localWord}
}

func (g *game) letterStates() map[byte]mark {
	states := map[byte]mark{}
	for row := 0; row < g.row; row++ {
		for col := 0; col < 5; col++ {
			letter := g.guesses[row][col]
			if m := g.marks[row][col]; m > states[letter] {
				states[letter] = m
			}
		}
	}
	return states
}

func keyTile(letter byte, m mark) string {
	content := " " + strings.ToUpper(string(letter)) + " "
	switch m {
	case markGreen:
		return bg(colGreen, fg(colWhite, bold+content))
	case markYellow:
		return bg(colYellow, fg(colWhite, bold+content))
	case markGray:
		return bg(colKeyAbsent, fg(colKeyAbsentFg, content))
	default:
		return bg(colKeyDefault, fg(colText, content))
	}
}

// centerUnderBoard centers text under the 24-wide tile grid (min 2-space indent).
func centerUnderBoard(plain, styled string) string {
	pad := max(2, (24-len(plain))/2)
	return strings.Repeat(" ", pad) + styled
}

func (g *game) renderKeyboard() string {
	// 3-char keys, no gap. Row widths: QWERTYUIOP=30, ASDFGHJKL=27, ZXCVBNM=21.
	// Pads chosen so each row centers under the 20-char board (4-space indent).
	rows := []string{"qwertyuiop", "asdfghjkl", "zxcvbnm"}
	pads := []string{"    ", "     ", "       "}
	states := g.letterStates()
	var sb strings.Builder
	for i, row := range rows {
		sb.WriteString(pads[i])
		for pos := 0; pos < len(row); pos++ {
			ch := row[pos]
			sb.WriteString(keyTile(ch, states[ch]))
		}
		sb.WriteString(nl)
	}
	return sb.String()
}

func (g *game) render() string {
	var sb strings.Builder
	sb.Grow(2048)
	sb.WriteString(clearScr)

	// header
	title := fg(colAccent, bold+"  W O R D L E")
	var subtitle string
	if g.localWord {
		subtitle = fg(colMuted, italic+"  random word") + "  " + fg(colYellow, "offline")
	} else {
		offlineBadge := ""
		if g.offline {
			offlineBadge = "  " + fg(colRed, "offline")
		}
		subtitle = fg(colMuted, italic+fmt.Sprintf("  #%d · %s", g.dayNum, g.date)) + offlineBadge
	}
	sb.WriteString(nl + title + nl)
	sb.WriteString(subtitle + nl)
	if g.offline && !g.localWord {
		sb.WriteString(fg(colRed, italic+"  could not reach NYT — playing with a random word") + nl)
	}
	sb.WriteString(nl)

	// board
	states := g.letterStates()
	for r := 0; r < 6; r++ {
		sb.WriteString("    ")
		for c := 0; c < 5; c++ {
			var letter byte
			var m mark
			if r < g.row {
				if c < len(g.guesses[r]) {
					letter = g.guesses[r][c]
				}
				m = g.marks[r][c]
			} else if r == g.row {
				if c < len(g.current) {
					letter = g.current[c]
					if states[letter] == markGray {
						m = markWarn
					} else {
						m = markPending
					}
				}
			}
			sb.WriteString(tile(letter, m))
			sb.WriteString(" ")
		}
		sb.WriteString(nl + nl)
	}

	// keyboard
	sb.WriteString(nl + g.renderKeyboard())

	// status
	if g.message != "" {
		sb.WriteString(g.message + nl)
	} else {
		sb.WriteString("  " + fg(colMuted, italic+"type a 5-letter word · enter to submit · esc to quit") + nl)
	}

	return sb.String()
}

func (g *game) revealRow(row int, marks [5]mark) {
	for col := 0; col < 5; col++ {
		g.marks[row][col] = markFlipping
		fmt.Print(g.render())
		time.Sleep(animFlipDelay)
		g.marks[row][col] = marks[col]
		fmt.Print(g.render())
		time.Sleep(animRevealDelay)
	}
}

func (g *game) shake() {
	for range 5 {
		fmt.Print(g.render())
		time.Sleep(animShakeDelay)
	}
}

func (g *game) celebrate() {
	for range 2 {
		fmt.Print(g.render())
		time.Sleep(animPulseDelay)
	}
}

func (g *game) shareText(won bool, rowsUsed int) string {
	score := "X"
	if won {
		score = strconv.Itoa(rowsUsed)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Wordle %d %s/6\n\n", g.dayNum, score))
	for r := 0; r < rowsUsed; r++ {
		for c := 0; c < 5; c++ {
			sb.WriteString(emojiFor(g.marks[r][c]))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func (g *game) play() (won bool, rowsUsed int) {
	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		fmt.Println("could not enter raw mode:", err)
		return
	}
	defer term.Restore(stdinFd, oldState)

	fmt.Print(hideCur)
	defer fmt.Print(showCur)
	fmt.Print(g.render())

	var pendingEsc bool
	for g.row < 6 {
		key, err := readKey()
		if err != nil {
			return false, g.row
		}
		if key == 0 {
			continue
		}

		switch {
		case key == 3: // ctrl-c
			return false, g.row

		case key == 27: // esc
			if pendingEsc {
				return false, g.row
			}
			pendingEsc = true
			g.message = centerUnderBoard("press ESC again to quit", fg(colMuted, italic+"press ESC again to quit"))
			fmt.Print(g.render())

		case key == 13 || key == 10: // enter
			pendingEsc = false
			if len(g.current) != 5 {
				g.message = centerUnderBoard("need 5 letters", fg(colYellow, italic+"need 5 letters"))
				g.shake()
				g.message = ""
				fmt.Print(g.render())
				continue
			}
			guess := strings.ToLower(g.current)
			if !validWords[guess] {
				g.message = centerUnderBoard("not a word", fg(colYellow, italic+"not a word"))
				g.shake()
				time.Sleep(time.Second)
				g.message = ""
				fmt.Print(g.render())
				continue
			}
			g.guesses[g.row] = guess
			marks := scoreGuess(guess, g.solution)
			// advance row before animating: render() checks r < g.row to decide
			// whether to read from g.marks (where markFlipping is set) vs g.current
			g.current = ""
			g.row++
			g.revealRow(g.row-1, marks)
			if isWin(marks) {
				cheer := cheers[g.row-1]
				g.message = centerUnderBoard(cheer, fg(colGreen, bold+cheer))
				g.celebrate()
				fmt.Print(g.render())
				return true, g.row
			}
			if g.row == 6 {
				sol := "the word was " + strings.ToUpper(g.solution)
				g.message = centerUnderBoard(sol, fg(colMuted, italic+"the word was "+bold+strings.ToUpper(g.solution)))
				fmt.Print(g.render())
				return false, 6
			}
			fmt.Print(g.render())

		case key == 127 || key == 8: // backspace
			pendingEsc = false
			g.message = ""
			if len(g.current) > 0 {
				g.current = g.current[:len(g.current)-1]
			}
			fmt.Print(g.render())

		case (key >= 'a' && key <= 'z') || (key >= 'A' && key <= 'Z'):
			pendingEsc = false
			g.message = ""
			if len(g.current) < 5 {
				if key >= 'A' && key <= 'Z' {
					key += 'a' - 'A'
				}
				g.current += string(key)
			}
			fmt.Print(g.render())
		}
	}
	return false, 6
}

func intro() {
	fmt.Print(hideCur)
	defer fmt.Print(showCur)

	letters := []string{"W", "O", "R", "D", "L", "E"}
	colors := [][3]int{colGreen, colYellow, colGray, colGreen, colYellow, colGreen}
	for count := 1; count <= len(letters); count++ {
		fmt.Print(clearScr)
		fmt.Print("\n\n   ")
		for i := 0; i < count; i++ {
			fmt.Print(bg(colors[i], fg(colWhite, bold+" "+letters[i]+" ")))
			fmt.Print(" ")
		}
		fmt.Println()
		time.Sleep(animIntroDelay)
	}
	time.Sleep(animIntroPause)
}

type puzzleEntry struct {
	dayNum int
	date   time.Time
}

func recentPuzzles(count int) []puzzleEntry {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	var entries []puzzleEntry
	for d := today; len(entries) < count; d = d.AddDate(0, 0, -1) {
		days := int(d.Sub(wordleLaunch).Hours() / 24)
		if days < 0 {
			break
		}
		entries = append(entries, puzzleEntry{dayNum: days, date: d})
	}
	return entries
}

func renderHistoryList(entries []puzzleEntry, cursor int) string {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	var sb strings.Builder
	sb.WriteString(clearScr + nl)
	sb.WriteString(fg(colAccent, bold+"  W O R D L E") + nl)
	sb.WriteString(fg(colMuted, italic+"  select a past puzzle") + nl + nl)
	for i, e := range entries {
		line := fmt.Sprintf("   #%-4d  %s", e.dayNum, e.date.Format("Jan 02, 2006"))
		todayMark := ""
		if e.date.Equal(today) {
			todayMark = "  " + fg(colAccent, "today")
		}
		if i == cursor {
			sb.WriteString(fg(colWhite, bold+" ▶"+line[2:]) + todayMark)
		} else {
			sb.WriteString(fg(colMuted, line) + todayMark)
		}
		sb.WriteString(nl)
	}
	sb.WriteString(nl + fg(colMuted, italic+"  ↑↓ navigate  ·  enter to play  ·  esc to cancel") + nl)
	return sb.String()
}

func historyBrowser() (date string, cancelled bool) {
	entries := recentPuzzles(historyCount)
	cursor := 0

	state, err := term.MakeRaw(stdinFd)
	if err != nil {
		return "", true
	}
	defer term.Restore(stdinFd, state)

	fmt.Print(renderHistoryList(entries, cursor))
	for {
		key, err := readKey()
		if err != nil {
			return "", true
		}
		switch {
		case key == 27 || key == 3:
			return "", true
		case key == keyArrowUp && cursor > 0:
			cursor--
			fmt.Print(renderHistoryList(entries, cursor))
		case key == keyArrowDown && cursor < len(entries)-1:
			cursor++
			fmt.Print(renderHistoryList(entries, cursor))
		case key == 13 || key == 10:
			return entries[cursor].date.Format("2006-01-02"), false
		}
	}
}

func postGameMenu() string {
	fmt.Print(fg(colMuted, italic+"  esc to exit  ·  h for history  ·  r for random\n"))
	state, err := term.MakeRaw(stdinFd)
	if err != nil {
		return "exit"
	}
	defer term.Restore(stdinFd, state)
	for {
		key, _ := readKey()
		switch {
		case key == 27 || key == 3:
			return "exit"
		case key == 'h' || key == 'H':
			return "history"
		case key == 'r' || key == 'R':
			return "random"
		}
	}
}

// randomWord picks from the local fallback list when the NYT API is unavailable.
func randomWord() (string, int) {
	dayNum := int(time.Since(wordleLaunch).Hours() / 24)
	return answerWords[rand.Intn(len(answerWords))], dayNum
}

func main() {
	mode := "today"
	date := ""

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-r", "--random":
			mode = "random"
		case "-h", "--history":
			mode = "history"
		default:
			date = os.Args[1]
			mode = "date"
		}
	}

	firstGame := true
	for {
		var solution, displayDate string
		var dayNum int
		var offline, useLocalWord bool

		if mode == "history" {
			selectedDate, cancelled := historyBrowser()
			if cancelled {
				return
			}
			date = selectedDate
			mode = "date"
		}

		if mode == "random" {
			yesterday := time.Now().AddDate(0, 0, -1)
			totalDays := int(yesterday.Sub(wordleLaunch).Hours() / 24)
			date = wordleLaunch.AddDate(0, 0, rand.Intn(totalDays+1)).Format("2006-01-02")
			fmt.Print(clearScr)
			fmt.Print(fg(colMuted, italic+"\n  fetching random puzzle...\n"))
			if resp, err := fetchSolution(date); err == nil {
				solution = resp.Solution
				dayNum = resp.DaysSinceLaunch
				displayDate = resp.PrintDate
			} else {
				solution, dayNum = randomWord()
				offline = true
				useLocalWord = true
			}
		} else {
			if mode == "today" {
				date = time.Now().Format("2006-01-02")
			}
			fmt.Print(clearScr)
			fmt.Print(fg(colMuted, italic+"\n  fetching puzzle...\n"))
			if resp, err := fetchSolution(date); err == nil {
				solution = resp.Solution
				dayNum = resp.DaysSinceLaunch
				displayDate = resp.PrintDate
			} else {
				fmt.Print(fg(colRed, fmt.Sprintf("\n  could not fetch puzzle for %s: %v\n", date, err)))
				fmt.Print("  play with a random word instead? [y/N] ")
				answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.ToLower(strings.TrimSpace(answer)) != "y" {
					return
				}
				solution, dayNum = randomWord()
				displayDate = date
				offline = true
			}
		}

		if firstGame {
			intro()
			firstGame = false
		}

		g := newGame(solution, dayNum, displayDate, offline, useLocalWord)
		won, rowsUsed := g.play()

		fmt.Println()
		fmt.Println(fg(colMuted, italic+"  ─── share ───"))
		fmt.Println()
		for _, line := range strings.Split(g.shareText(won, rowsUsed), "\n") {
			fmt.Println("  " + line)
		}
		fmt.Println()

		if next := postGameMenu(); next != "exit" {
			mode = next
		} else {
			return
		}
	}
}
