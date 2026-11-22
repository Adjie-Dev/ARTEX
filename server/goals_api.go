package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// Ringkasan: endpoint CRUD manual untuk "manajemen goal". Menulis ke batch node goal
// yang sama dengan tool set_goals di sisi agent, tetapi jalur masuknya adalah manusia
// yang langsung menambah/mengubah/menghapus lewat UI; setelah menambah/mengubah, logika
// "memulihkan task" dipakai ulang (admitTask resume: status akhir -> running, melepas
// pause, masuk antrean bila perlu), sedangkan hapus tidak memulihkan task (keputusan
// produk). Setiap handler perubahan melewati beginTaskOperation/decInflight, untuk
// menghindari race dengan penghapusan task (konsisten dengan CRUD intent).

// listGoals mengembalikan semua goal milik task ini (text/vulnclass/state sudah
// diurai), untuk dirender kartu manajemen goal.
func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	goals, err := t.Store.ListByKind(db.KindGoal, 10000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"goals": goalDTOs(goals)})
}

// addGoal menambahkan satu goal secara manual: disimpan ke database (digantung di
// bawah spawns akar task), lalu mencatat satu event "goal ditambahkan" yang memicu
// planner untuk bangun, kemudian memulihkan task agar planner menilai ulang
// tercapai atau tidaknya berdasarkan goal baru.
func (s *Server) addGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "task sedang dihapus, tidak bisa menambahkan goal")
		return
	}
	defer s.engine.decInflight(t.ID)

	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "isi goal tidak boleh kosong")
		return
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(body.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	id, err := t.Store.AddGoal(payload, "human")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if of, _ := t.Store.OriginFactID(); of > 0 && id > 0 {
		_ = t.Store.Link(of, db.RelSpawns, id) // goal descends from the task root (origin fact)
	}
	t.NotifyGoal([]string{text}) // mencatat "pengguna menambahkan goal: ..." yang memicu dan membangunkan planner
	s.reviveTask(t)              // menarik task yang sudah selesai/dipause kembali ke status berjalan
	node, _ := t.Store.GetNode(id)
	if node == nil {
		writeErr(w, 500, "gagal membaca kembali setelah goal ditulis")
		return
	}
	writeJSON(w, 200, goalDTO(node))
}

// editGoal mengubah teks (dan vulnclass) satu goal secara manual: mengubah database,
// lalu mencatat event "pengguna mengubah goal dari lama menjadi baru" yang memicu
// planner untuk bangun, kemudian memulihkan task agar planner menyesuaikan arah
// berdasarkan goal baru.
func (s *Server) editGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "task sedang dihapus, tidak bisa mengubah goal")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "isi goal tidak boleh kosong")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "goal tidak ditemukan")
		return
	}
	oldText := goalDTO(node).Text
	if err := t.Store.UpdateGoalPayload(gid, text, strings.TrimSpace(body.VulnClass)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalEdited(oldText, text) // mencatat "pengguna mengubah goal dari lama menjadi baru" yang memicu dan membangunkan planner
	s.reviveTask(t)                   // konsisten dengan penambahan: memulihkan task agar dinilai ulang berdasarkan goal baru
	updated, _ := t.Store.GetNode(gid)
	if updated == nil {
		writeErr(w, 500, "gagal membaca kembali setelah goal diperbarui")
		return
	}
	writeJSON(w, 200, goalDTO(updated))
}

// deleteGoal menghapus satu goal secara manual (hard delete, edge/anchor ikut
// terhapus lewat cascade): menghapus dari database, lalu mencatat event "pengguna
// menghapus goal X" yang memicu planner menilai ulang goal yang tersisa. Sesuai
// keputusan produk, penghapusan [tidak] memulihkan task.
func (s *Server) deleteGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "task sedang dihapus, tidak bisa menghapus goal")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "goal tidak ditemukan")
		return
	}
	text := goalDTO(node).Text
	if err := t.Store.DeleteGoal(gid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalDeleted(text) // mencatat "pengguna menghapus goal: ..." yang memicu dan membangunkan planner (tanpa memulihkan task)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
