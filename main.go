package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---------- Model ----------

// This is the structure that defines each bookmark
type Bookmark struct {
	ID          int       `json:"id"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Tags        []string  `json:"tags"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// bookmarkInput is the payload accepted by POST.
type bookmarkInput struct {
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// bookmarkPatch is the payload accepted by PUT and PATCH on /bookmarks/{id}.
type bookmarkPatch struct {
	URL         *string   `json:"url"`
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Tags        *[]string `json:"tags"`
}

// validationError marks an error as "the data is invalid" (422) rather than a storage failure (500)
type validationError struct{ error }

// This validates an incoming bookmark input and edits the orginal data if needed
func (in *bookmarkInput) validate() error {

	// This removes any leading or trailing spaces that may come from the URL or Title
	in.URL = strings.TrimSpace(in.URL)
	in.Title = strings.TrimSpace(in.Title)

	// This checks if the URL or Title is blank
	if in.URL == "" {
		return errors.New("url is required")
	}
	if in.Title == "" {
		return errors.New("title is required")
	}

	// Parses a raw URL into a URL structure while checking that it is in the correct format
	u, err := url.Parse(in.URL)

	// Returns an error if the url is in the incorrect format
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url must be a valid http or https URL")
	}

	// Normalize tags: trim, lowercase, dedupe, drop empties.

	// Creates a map for seen tags and a slice for all tags
	seen := map[string]bool{}
	tags := make([]string, 0, len(in.Tags))

	// Loops through every tag in the provided task list
	for _, t := range in.Tags {

		// Sets the tag to lowercase and removes spaces
		t = strings.ToLower(strings.TrimSpace(t))

		// If the tag is not blank and has not been marked as seen, mark it as seen and add it to the tags list
		if t != "" && !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}

	// Set the datas tag list to our Normalized one
	in.Tags = tags
	return nil
}

// ---------- Store ----------

// This structure is an in-memory database for the bookmarks
type Store struct {
	mu     sync.RWMutex     // Reader and writer lock (lets people read simultaneously but not write)
	items  map[int]Bookmark // A map with all our bookmarks
	nextID int              // Tracks the next ID needed for the map
	path   string           // empty = no persistence (file lost), needs a file to save to memory
}

// FileData is the on-disk format. Saving next_id means IDs of deleted bookmarks are never handed out again, even after a restart
type fileData struct {
	NextID    int        `json:"next_id"`
	Bookmarks []Bookmark `json:"bookmarks"`
}

// Initializes a newStore object with a filepath passed as a parameter
func NewStore(path string) (*Store, error) {

	// Creates an empty store object
	s := &Store{items: make(map[int]Bookmark), nextID: 1, path: path}

	// If there is no path, then return null as the error
	if path == "" {
		return s, nil
	}

	// Reads the path given by the user
	data, err := os.ReadFile(path)

	// If it does not exist, then return null as the error
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}

	// If a different error is recieved upon reading the file, return it
	if err != nil {
		return nil, err
	}

	// An empty file is treated the same as a missing one
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return s, nil
	}

	// Holds the loaded file contents
	var fd fileData

	// Unmarshal reads data from the file to the list sending an error if it fails
	if err := json.Unmarshal(data, &fd); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// Restores the saved counter (never lower than 1)
	if fd.NextID > s.nextID {
		s.nextID = fd.NextID
	}

	// Loops through every bookmark object found in the list
	for _, b := range fd.Bookmarks {
		// Adds each book mark to the items map within the Store object
		s.items[b.ID] = b

		// Resets the next ID of the store object if needed
		if b.ID >= s.nextID {
			s.nextID = b.ID + 1
		}
	}

	// Returns the created store object and null
	return s, nil
}

// save writes all bookmarks to disk atomically. Caller must hold the lock.
func (s *Store) save() error {

	// If the Store object has no path, return null
	if s.path == "" {
		return nil
	}

	// This makes a slice for each item within the Store object
	list := make([]Bookmark, 0, len(s.items))

	// Adds all the items in the Store object to the list
	for _, b := range s.items {
		list = append(list, b)
	}

	// Sorts the slice in ascending order by ID
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })

	// Converts the list of Bookmark objects into JSON
	data, err := json.MarshalIndent(fileData{NextID: s.nextID, Bookmarks: list}, "", "  ")
	if err != nil {
		return err
	}

	// Writes to a temporary file returning an error if it arises
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}

	// Swaps the temporary file to the actual file
	return os.Rename(tmp, s.path)
}

// This function helps search and filter through the bookmarks by tag and search query
func (s *Store) List(tag, q string) []Bookmark {

	// Sets a read lock that allows for multiple people read from at the same time blocking the users from writing to the file
	s.mu.RLock()

	// Ensures that the read lock is lifted once this function finishes executing
	defer s.mu.RUnlock()

	// Converts search parameters to lowercase
	tag = strings.ToLower(tag)
	q = strings.ToLower(q)

	// Initializes an empty slice to store matching bookmarks
	out := make([]Bookmark, 0, len(s.items))

	// Loops through each bookmark in the store object
	for _, b := range s.items {

		// Checks if the bookmark contains the tag, if not continue to the next object
		if tag != "" && !hasTag(b.Tags, tag) {
			continue
		}

		// Checks if the query arrise in the bookmark by combining its Title URL and Description into one string than using the contains function, if not continue to the next object
		if q != "" {
			hay := strings.ToLower(b.Title + " " + b.URL + " " + b.Description)
			if !strings.Contains(hay, q) {
				continue
			}
		}

		// Adds the bookmark to the out slice
		out = append(out, b)
	}

	// Sorts the out slice in ascending order
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	// Returns the out slice populated with matching bookmarks
	return out
}

// Checks if a list of tags contains a tag
func hasTag(tags []string, tag string) bool {

	// Checks if slice contains a tag
	return slices.Contains(tags, tag)
}

// This returns a bookmark from a store object
func (s *Store) Get(id int) (Bookmark, bool) {

	// Sets a read lock
	s.mu.RLock()
	// Unlocks the read lock upon function execution
	defer s.mu.RUnlock()

	// Checks if the ID is present within the Store object items, if not it returns false
	b, ok := s.items[id]
	return b, ok
}

// Creates a new bookmark
func (s *Store) Create(in bookmarkInput) (Bookmark, error) {

	// Activates a write lock, forcing all other incoming requests to wait
	s.mu.Lock()
	// Unlocks the write lock upon function execution
	defer s.mu.Unlock()

	// Captures the current timestamp
	now := time.Now().UTC()

	// Makes a new bookmark object using the nextID in the store object and the data provided from the in parameter
	b := Bookmark{
		ID: s.nextID, URL: in.URL, Title: in.Title, Description: in.Description,
		Tags: in.Tags, CreatedAt: now, UpdatedAt: now,
	}

	// Adds the new bookmark to the items list within the Store object
	s.items[b.ID] = b

	// Increments the nextID value
	s.nextID++

	// Saves the store object, if an error occurs it everything that we did will be undone
	if err := s.save(); err != nil {
		delete(s.items, b.ID)
		s.nextID--
		return Bookmark{}, err
	}

	// Returns our finished bookmark object and a nil error
	return b, nil
}

// This updates a bookmark given an ID and a bookmarkInput, it returns the updated bookmark, a boolean that states whether or not the ID was found, and an error
func (s *Store) Update(id int, p bookmarkPatch) (Bookmark, bool, error) {

	// Activates write lock
	s.mu.Lock()
	// Deactivates write lock upon function execution
	defer s.mu.Unlock()

	// Gets the old bookmark from the Store Objects item list using the id, returns an error if the bookmark is not found
	old, ok := s.items[id]
	if !ok {
		return Bookmark{}, false, nil
	}

	// Starts from the old values (cloning the tags so we never share the slice with the old bookmark)
	in := bookmarkInput{URL: old.URL, Title: old.Title, Description: old.Description, Tags: slices.Clone(old.Tags)}

	// Overwrites only the fields the client actually sent
	if p.URL != nil {
		in.URL = *p.URL
	}
	if p.Title != nil {
		in.Title = *p.Title
	}
	if p.Description != nil {
		in.Description = *p.Description
	}
	if p.Tags != nil {
		in.Tags = *p.Tags
	}

	// Validates the merged result, this is done here (under the lock) so the check and the write see the same data
	if err := in.validate(); err != nil {
		return Bookmark{}, true, validationError{err}
	}

	// Sets b to the old bookmark
	b := old

	// Sets the values of b to the values of the bookmarkInput object parameter
	b.URL, b.Title, b.Description, b.Tags = in.URL, in.Title, in.Description, in.Tags

	// Updates the updated at variable
	b.UpdatedAt = time.Now().UTC()

	// Saves b to the items list within the store object, undoing what we have changed and returning an error if it fails
	s.items[id] = b
	if err := s.save(); err != nil {
		s.items[id] = old
		return Bookmark{}, true, err
	}

	// Returns our updated bookmark object and a nil error
	return b, true, nil
}

// This function removes a bookmark from the items list in our store object based on its ID, it returns true if successful and an error
func (s *Store) Delete(id int) (bool, error) {

	// Activates write lock
	s.mu.Lock()

	// Deactivates write lock upon function execution
	defer s.mu.Unlock()

	// Saves the old bookmark object within the items list as a variable, if it fails the ID was not found
	old, ok := s.items[id]
	if !ok {
		return false, nil
	}

	// Removes the bookmark with the passed ID from our items list, undoing the deletion and returning an error if it fails
	delete(s.items, id)
	if err := s.save(); err != nil {
		s.items[id] = old
		return true, err
	}

	// Returns true if the ID was successfully deleted
	return true, nil
}

// ---------- HTTP helpers ----------

// This function helps write the JSON using the network connection, it also takes a status code from that network connection. The any variable represent what you are writing
func writeJSON(w http.ResponseWriter, status int, v any) {

	// This sets the header being sent while also telling it that the data coming through is json
	w.Header().Set("Content-Type", "application/json")

	// Sends the status code as well as the header through the network
	w.WriteHeader(status)

	// Checks whether or not the variable is empty
	if v != nil {

		// This makes an encoder that is able to write through the network, encode turns the variable into Json and then writes it
		if err := json.NewEncoder(w).Encode(v); err != nil {

			// This prints the error to the log if there is one
			log.Printf("encode response: %v", err)
		}
	}
}

// This writes an error through the network it also takes the status code from that network and sends it
func writeError(w http.ResponseWriter, status int, msg string) {

	// This creates a map to send the error with, makes sure that each error looks the same {"error": msg}
	writeJSON(w, status, map[string]string{"error": msg})
}

// This takes an incoming request from the network and converts it
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {

	// Makes reading stop after 1 mib so that a giant payload is not sent through, exhausting the memory
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB

	// Creates a decoder that can read JSON from the body
	dec := json.NewDecoder(r.Body)

	// If the JSON has a field that does not belong in our structure, it will throw an error
	dec.DisallowUnknownFields()

	// Checks if the body is in valid JSON format while decoding the data to dst
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}

	// More checks if there is more data following the first JSON value, if so throw an error
	if dec.More() {
		return errors.New("invalid JSON body: unexpected trailing data")
	}

	// This means the function worked succesfully, no error
	return nil
}

// Parses the ID returning that ID and a boolean saying if the function worked or not
func parseID(w http.ResponseWriter, r *http.Request) (int, bool) {

	// This takes the "ID" part of the path and isolates it as a string variable, ATOI turns the string into an integer
	id, err := strconv.Atoi(r.PathValue("id"))

	// Checks if the ID is a number and if it is greater than or equal to 1
	if err != nil || id < 1 {

		// Sends an status error and then returns false showing that the function failed
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return 0, false
	}

	// Returns the ID and returns true showing the function worked
	return id, true
}

// ---------- Handlers ----------

// This is a small container that gives the api access to a store object
type API struct{ store *Store }

// This builds a router that registers each endpoint and then returns the finished handler
func (a *API) routes() http.Handler {
	// Creates the router
	mux := http.NewServeMux()

	// Crud Functions
	mux.HandleFunc("GET /bookmarks", a.list)
	mux.HandleFunc("POST /bookmarks", a.create)
	mux.HandleFunc("GET /bookmarks/{id}", a.get)
	mux.HandleFunc("PUT /bookmarks/{id}", a.update)
	mux.HandleFunc("PATCH /bookmarks/{id}", a.update)
	mux.HandleFunc("DELETE /bookmarks/{id}", a.delete)

	// Function that gives a health check
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Wraps the handler in a logging middleware so that each request is logged
	return logging(mux)
}

// Handles GET /bookmarks to retrieve all bookmark data
func (a *API) list(w http.ResponseWriter, r *http.Request) {

	// Parse the URL query string into a map-like value
	q := r.URL.Query()

	// Checks for matching tags of queries and sends them back as JSON
	writeJSON(w, http.StatusOK, a.store.List(q.Get("tag"), q.Get("q")))
}

// Handles Post /bookmarks to create new bookmark
func (a *API) create(w http.ResponseWriter, r *http.Request) {
	// This variable will receive bookmark data
	var in bookmarkInput

	// Decodes the input
	if err := decodeBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Validates the input
	if err := in.validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Creates a new bookmark within the store
	b, err := a.store.Create(in)
	if err != nil {
		log.Printf("create: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save bookmark")
		return
	}

	// Tells the client where the new bookmark is located
	w.Header().Set("Location", fmt.Sprintf("/bookmarks/%d", b.ID))

	// Respond 201 with the new bookmark as the json body
	writeJSON(w, http.StatusCreated, b)
}

// Handles GET /bookmarks/{id} to retrive a bookmark
func (a *API) get(w http.ResponseWriter, r *http.Request) {
	// Parses the list for the ID value
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	// Finds the bookmark within the list
	b, found := a.store.Get(id)
	if !found {
		writeError(w, http.StatusNotFound, "bookmark not found")
		return
	}

	// Respond 200 ok with the bookmark
	writeJSON(w, http.StatusOK, b)
}

// Handles PUT /bookmarks/{id} to change a bookmark
func (a *API) update(w http.ResponseWriter, r *http.Request) {
	// Parses the list for the ID value
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	// This variable will receive the bookmark data
	var p bookmarkPatch

	// Decodes the input
	if err := decodeBody(w, r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Updates the bookmark
	b, found, err := a.store.Update(id, p)
	if !found {
		writeError(w, http.StatusNotFound, "bookmark not found")
		return
	}

	// The merged bookmark was not valid, e.g. "title": "" or a bad url
	var verr validationError
	if errors.As(err, &verr) {
		writeError(w, http.StatusUnprocessableEntity, verr.Error())
		return
	}
	if err != nil {
		log.Printf("update: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save bookmark")
		return
	}

	// Respond 200 ok with the bookmark
	writeJSON(w, http.StatusOK, b)
}

// Handles DELETE /bookmarks/{id} to remove a bookmark
func (a *API) delete(w http.ResponseWriter, r *http.Request) {
	// Parses the list for the ID value
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	// Removes an ID from the store list
	found, err := a.store.Delete(id)
	if !found {
		writeError(w, http.StatusNotFound, "bookmark not found")
		return
	}
	if err != nil {
		log.Printf("delete: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save changes")
		return
	}

	// Respond 204 with no content
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Middleware ----------

// Has the ResponseWriter embedded in it and stores status codes
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// Overrides the WriteHeader function, saving the status code and then using the original function
func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Takes the handler function and then return a handler that stores extra data
func logging(next http.Handler) http.Handler {

	// Turns a plain function into one with a ServeHTTP method
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		// Records start time
		start := time.Now()

		// Wraps the writer in a recorder
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		// Runs the actual handler but passes the status recorder instead of the standared writer
		next.ServeHTTP(rec, r)

		// Prints the Method, the path, the status and time values
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Microsecond))
	})
}

// ---------- Main ----------

// Used for reading configuration from environment variables with a default value
func getenv(key, fallback string) string {

	// Returns the variable's value if it is not empty
	if v := os.Getenv(key); v != "" {
		return v
	}

	// Returns the default if the variable is empty
	return fallback
}

// Wires up the store and API and then serves until interrupted
func main() {

	// Uses getenv helper to get the listen address and data file
	addr := getenv("ADDR", ":8080")
	dataFile := getenv("DATA_FILE", "bookmarks.json")

	// Opens a new store based on the info in the data file
	store, err := NewStore(dataFile)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	// Builds a http.Server explicitly, needed for timeouts and graceful shutdowns
	srv := &http.Server{
		Addr:              addr,
		Handler:           (&API{store: store}).routes(), // Creates the API with the store and logging router
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Creates a context that is cancelled when the process receives a sigint or sigterm
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Shutsdown server upon receiving the http.errserverclosed message
	go func() {
		log.Printf("listening on %s (data: %s)", addr, dataFile)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	// Main clocks here until a signal cancels the context
	<-ctx.Done()

	// Shuts down the server and stops accepting new request after 5 seconds any requests after that return a deadline error
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
