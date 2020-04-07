package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/dgrijalva/jwt-go"
	"github.com/gorilla/mux"
	"github.com/lithammer/shortuuid"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"

	"server/models"
)

var db *sql.DB
var err error

const DB_URL string = "./test.db"
const JWT_SECRET string = "password"

func main() {
	// Initialise the global DB pool
	db, err = sql.Open("sqlite3", DB_URL)
	if err != nil {
		panic(err.Error())
	}

	defer db.Close()

	// Initialise the router
	r := mux.NewRouter()

	// Unauthenticated endpoints
	r.HandleFunc("/api/v1/login", loginUser).Methods("POST", "OPTIONS")
	r.HandleFunc("/api/v1/register", registerUser).Methods("POST")

	auth := r.PathPrefix("/api/v1").Subrouter()

	auth.HandleFunc("/users/self", getUserById).Methods("GET", "OPTIONS")
	auth.HandleFunc("/users", getAllAccessibleUsers).Methods("GET", "OPTIONS")

	auth.HandleFunc("/tasks", getTasks).Methods("GET", "OPTIONS")
	auth.HandleFunc("/tasks", createTask).Methods("POST", "OPTIONS")
	auth.HandleFunc("/tasks", updateTask).Methods("PUT", "OPTIONS")
	auth.HandleFunc("/tasks", deleteTask).Methods("DELETE", "OPTIONS")

	r.Use(corsMiddleware)
	auth.Use(authMiddleware)

	fmt.Printf("All setup running, and available on port 8000")

	log.Fatal(http.ListenAndServe(":8000", r))
}

//---------------------- MIDDLEWARES ------------------------------//
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(200)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqToken := r.Header.Get("Authorization")

		if reqToken == "" {
			http.Error(w, "No auth token", http.StatusForbidden)
			return
		}

		splitToken := strings.Split(reqToken, "Bearer")

		if len(splitToken) != 2 {
			http.Error(w, "Malformed format for auth token", http.StatusForbidden)
			return
		}

		reqToken = strings.TrimSpace(splitToken[1])

		parsedToken, err := jwt.Parse(reqToken, func(token *jwt.Token) (interface{}, error) {
			// Don't forget to validate the alg is what you expect
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, errors.New("Invalid Signing Type")
			}

			return []byte(JWT_SECRET), nil
		})

		// Invalid JWT secret error
		if err != nil {
			http.Error(w, "Authentication failed", http.StatusForbidden)
			return
		}

		// Parsing the claims in the JWT token
		if claims, ok := parsedToken.Claims.(jwt.MapClaims); ok && parsedToken.Valid {
			// If the claims doesn't include the Id or the UserType, throw an error
			if claims["id"] == nil || claims["type"] == nil {
				http.Error(w, "Authentication claims failed", http.StatusForbidden)
				return
			}

			uid := claims["id"].(string)
			utype := claims["type"].(string)

			r.Header.Set("X-User-Claim", uid)
			r.Header.Set("X-User-Type", utype)

			next.ServeHTTP(w, r)
		} else {
			http.Error(w, "Auth token invalid", http.StatusForbidden)
			return
		}
	})
}

//------------------------------ HANDLERS (Login) ----------------------------------//
// These handlers specifically bypass the authentication middleware, because they do not need any verification
type loginUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type registerUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Type     string `json:"type"`
	Amb      int    `json:"amb"`
	Depot    int    `json:"depot"`
	Platoon  int    `json:"platoon"`
	Section  int    `json:"section"`
	Man      int    `json:"man"`
	Name     string `json:"name"`
}

func loginUser(w http.ResponseWriter, r *http.Request) {
	var req loginUserRequest

	// Decode the request
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var uid string
	var passwordhash string
	var utype string

	// Get the user associated to the username if it exists
	sql := `SELECT user, password_hash, type FROM user WHERE username = ?`
	if err := db.QueryRow(sql, req.Username).Scan(&uid, &passwordhash, &utype); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check if password hashes match then generate JWT
	if CheckPasswordHash(req.Password, passwordhash) {
		token, err := createJWT(uid, utype, JWT_SECRET)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Write([]byte(token))
	} else {
		// Password incorrect, throw unauthorized error
		http.Error(w, "Incorrect password", http.StatusUnauthorized)
		return
	}
}

func registerUser(w http.ResponseWriter, r *http.Request) {
	var req registerUserRequest

	// Decode the request
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Generate unique uid
	uid := shortuuid.New()

	// Create the SQL prepared statement
	sql := `INSERT INTO user VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	stmt, err := db.Prepare(sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	//The existence of the actual content of the parsed request does not need to be checked as it is verified by the NOT NULL constraints

	// Generate the password hash
	passwordhash, err := HashPassword(req.Password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Execute the statement
	_, err = stmt.Exec(uid, req.Username, passwordhash, req.Type, req.Amb, req.Depot, req.Platoon, req.Section, req.Man, req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return the new JWT
	token, err := createJWT(uid, req.Type, JWT_SECRET)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write([]byte(token))
}

//----------------------------- HANDLERS (User) ------------------------------------//
func getUserById(w http.ResponseWriter, r *http.Request) {
	// Get the current user id
	uid := r.Header.Get("X-User-Claim")

	var user models.User

	// Get the user associated to the id if it exists
	sql := `SELECT user, username, type, amb, depot, platoon, section, man, name FROM user WHERE user = ?`
	if err := db.QueryRow(sql, uid).Scan(&user.Id, &user.Username, &user.Utype, &user.Amb, &user.Depot, &user.Platoon, &user.Section, &user.Man, &user.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Marshal to JSON and return
	res, err := json.Marshal(user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(res)
}

func getAllAccessibleUsers(w http.ResponseWriter, r *http.Request) {
	uid := r.Header.Get("X-User-Claim")
	utype := r.Header.Get("X-User-Type")

	if utype != "admin" {
		http.Error(w, "No admin permissions for this user", http.StatusForbidden)
		return
	}

	// Get the platoon and section of the admin user
	amb, depot, platoon, section, err := getUserPrivileges(uid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var users []models.User

	// Get all the users under the admin user
	sql := `SELECT user, username, type, amb, depot, platoon, section, man, name FROM user WHERE type = "normal" AND `
	addAdminFilters(&sql, amb, depot, platoon, section)

	result, err := db.Query(sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	defer result.Close()

	for result.Next() {
		var user models.User
		if err := result.Scan(&user.Id, &user.Username, &user.Utype, &user.Amb, &user.Depot, &user.Platoon, &user.Section, &user.Man, &user.Name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		users = append(users, user)
	}

	// Marshal to JSON and return
	res, err := json.Marshal(users)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(res)
}

//---------------------------- HANDLERS (Task) ------------------------------------//
func getTasks(w http.ResponseWriter, r *http.Request) {
	uid := r.Header.Get("X-User-Claim")
	utype := r.Header.Get("X-User-Type")

	var tasks []models.Task

	if utype == "normal" {
		// Get all the tasks under this user
		sql := `SELECT task, name, assigned_to, completed, verified FROM task WHERE assigned_to = ?`

		results, err := db.Query(sql, uid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		defer results.Close()

		for results.Next() {
			var task models.Task
			if err := results.Scan(&task.Id, &task.Name, &task.AssignedTo, &task.Completed, &task.Verified); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			tasks = append(tasks, task)
		}
	} else if utype == "admin" {
		// Get the platoon and section of the admin user
		amb, depot, platoon, section, err := getUserPrivileges(uid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Get all the tasks under this admin
		sql := `SELECT task.task, task.name, task.assigned_to, task.completed, task.verified 
		FROM task INNER JOIN user ON user.user = task.assigned_to 
		WHERE type = "normal" AND `
		addAdminFilters(&sql, amb, depot, platoon, section)

		results, err := db.Query(sql, platoon, section)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		defer results.Close()

		for results.Next() {
			var task models.Task
			if err := results.Scan(&task.Id, &task.Name, &task.AssignedTo, &task.Completed, &task.Verified); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			tasks = append(tasks, task)
		}
	}

	// Return the full task list
	// Marshal to JSON and return
	res, err := json.Marshal(tasks)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(res)
}

type createTaskRequest struct {
	Name       string `json:"name"`
	AssignedTo string `json:"assigned_to"`
}

func createTask(w http.ResponseWriter, r *http.Request) {
	// Check the admin privileges
	uid := r.Header.Get("X-User-Claim")
	utype := r.Header.Get("X-User-Type")

	if utype != "admin" {
		http.Error(w, "No admin permissions for this user", http.StatusForbidden)
		return
	}

	// Get the platoon and section of the admin user
	aamb, adepot, aplatoon, asection, err := getUserPrivileges(uid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var req createTaskRequest

	// Decode the request
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	uamb, udepot, uplatoon, usection, err := getUserPrivileges(req.AssignedTo)

	// Check if admin has enough privileges to assign tasks to this user
	if aamb == uamb &&
		(adepot == udepot || adepot == -1) &&
		(aplatoon == uplatoon || aplatoon == -1) &&
		(asection == usection || asection == -1) {
		// Create the task
		tuid := shortuuid.New()

		// Create the SQL prepared statement
		sql := `INSERT INTO task VALUES (?, ?, ?, false, false)`
		stmt, err := db.Prepare(sql)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Execute the statement
		_, err = stmt.Exec(tuid, req.Name, req.AssignedTo)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		http.Error(w, "Insufficient admin permissions for this user", http.StatusForbidden)
		return
	}

	w.Write([]byte("Created task successfully"))
}

type deleteTaskRequest struct {
	Id string `json:"id"`
}

func deleteTask(w http.ResponseWriter, r *http.Request) {
	// Check the admin privileges
	uid := r.Header.Get("X-User-Claim")
	utype := r.Header.Get("X-User-Type")

	if utype != "admin" {
		http.Error(w, "No admin permissions for this user", http.StatusForbidden)
		return
	}

	// Get the platoon and section of the admin user
	aamb, adepot, aplatoon, asection, err := getUserPrivileges(uid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var req deleteTaskRequest
	var task models.Task

	// Decode the request
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Retrive the task information
	sql := `SELECT * FROM task WHERE task = ?`
	if err := db.QueryRow(sql, req.Id).Scan(&task.Id, &task.Name, &task.AssignedTo, &task.Completed, &task.Verified); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Retrive assignee information
	uamb, udepot, uplatoon, usection, err := getUserPrivileges(task.AssignedTo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Check if admin has enough privileges to remove tasks from this user
	if aamb == uamb &&
		(adepot == udepot || adepot == -1) &&
		(aplatoon == uplatoon || aplatoon == -1) &&
		(asection == usection || asection == -1) {

		// Create the SQL prepared statement
		sql := `DELETE FROM task WHERE task = ?`
		stmt, err := db.Prepare(sql)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Execute the statement
		_, err = stmt.Exec(task.Id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		http.Error(w, "Insufficient admin permissions for this user", http.StatusForbidden)
		return
	}

	w.Write([]byte("Deleted task successfully"))

}

type updateTaskRequest struct {
	Id         string `json:"id"`
	Name       string `json:"name"`
	AssignedTo string `json:"assigned_to"`
	Completed  bool   `json:"completed"`
	Verified   bool   `json:"verified"`
}

func updateTask(w http.ResponseWriter, r *http.Request) {
	var req updateTaskRequest
	var task models.Task

	// Decode the request
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Retrive the task information
	sql := `SELECT * FROM task WHERE task = ?`
	if err := db.QueryRow(sql, req.Id).Scan(&task.Id, &task.Name, &task.AssignedTo, &task.Completed, &task.Verified); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Check privilege of accessing request
	uid := r.Header.Get("X-User-Claim")
	utype := r.Header.Get("X-User-Type")

	if utype == "normal" {
		if uid != task.AssignedTo {
			http.Error(w, "This user doesn't have admin permissions", http.StatusForbidden)
			return
		}

		// Normal user doesn't have access to update these fields
		if req.Name != task.Name || req.AssignedTo != task.AssignedTo || req.Verified != task.Verified {
			http.Error(w, "This user doesn't have permissions to update these fields", http.StatusForbidden)
			return
		}

		task.Completed = req.Completed
	} else if utype == "admin" {
		// Check if admin user has access to this task assignee
		aamb, adepot, aplatoon, asection, err := getUserPrivileges(uid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Retrive assignee information
		uamb, udepot, uplatoon, usection, err := getUserPrivileges(task.AssignedTo)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Check if admin has enough privileges to update tasks from this user
		if aamb == uamb &&
			(adepot == udepot || adepot == -1) &&
			(aplatoon == uplatoon || aplatoon == -1) &&
			(asection == usection || asection == -1) {

			task.Name = req.Name
			task.AssignedTo = req.AssignedTo
			task.Completed = req.Completed
			task.Verified = req.Verified

		} else {
			http.Error(w, "This user doesn't have admin permissions", http.StatusForbidden)
			return
		}
	} else {
		http.Error(w, "Unknown exception", http.StatusInternalServerError)
		return
	}

	// Update the database with the task
	sql = `UPDATE task SET name = ?, assigned_to = ?, completed = ?, verified = ? WHERE task = ?`
	stmt, err := db.Prepare(sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Execute the statement
	_, err = stmt.Exec(task.Name, task.AssignedTo, task.Completed, task.Verified, task.Id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write([]byte("Task updated successfully"))
}

//------------------------ UTILITIES -----------------------------------------------//
func createJWT(uid string, utype string, secret string) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":   uid,
		"type": utype,
	})
	tokenString, err := token.SignedString([]byte(secret))

	return tokenString, err
}

func getUserPrivileges(uid string) (int, int, int, int, error) {
	var amb int
	var depot int
	var platoon int
	var section int

	sql := `SELECT amb, depot, platoon, section FROM user WHERE user = ?`
	if err := db.QueryRow(sql, uid).Scan(&amb, &depot, &platoon, &section); err != nil {
		return amb, depot, platoon, section, err
	}

	return amb, depot, platoon, section, nil
}

func addAdminFilters(sql *string, amb int, depot int, platoon int, section int) {
	*sql += fmt.Sprintf("amb = %d", amb)
	if depot != -1 {
		*sql += " AND "
		*sql += fmt.Sprintf("depot = %d", depot)
	}
	if platoon != -1 {
		*sql += " AND "
		*sql += fmt.Sprintf("platoon = %d", platoon)
	}
	if section != -1 {
		*sql += " AND "
		*sql += fmt.Sprintf("section = %d", section)
	}
}

func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	return string(bytes), err
}

func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}
