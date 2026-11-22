"use client";

import * as React from "react";

import { RotateCcwIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { FindingRetest } from "@/lib/types";

interface FindingRetestDialogProps {
  findingId: string;
  findingName?: string;
  onClose: () => void;
  onStarted?: (retest: FindingRetest) => void;
}

// Hanya dipasang saat terbuka, catatan dikosongkan setelah ditutup; daftar dan detail berbagi lock submit dan penanganan error, tetap di halaman saat ini setelah dimulai.
export function FindingRetestDialog({ findingId, findingName, onClose, onStarted }: FindingRetestDialogProps) {
  const notesId = React.useId();
  const [notes, setNotes] = React.useState("");
  const [submitting, setSubmitting] = React.useState(false);
  const submitLock = React.useRef(false);

  async function start() {
    if (submitLock.current) return;
    submitLock.current = true;
    setSubmitting(true);
    try {
      const result = await api.startFindingRetest(findingId, notes.trim());
      onStarted?.(result.retest);
      onClose();
      toast.success(result.created ? 'Uji ulang dimulai, klik "Sedang Diuji Ulang" untuk melihat sesinya' : "Kerentanan ini sedang diuji ulang, bisa lihat sesi yang sudah ada");
    } catch (e) {
      toast.error(`Gagal memulai uji ulang: ${(e as Error).message}`);
    } finally {
      submitLock.current = false;
      setSubmitting(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && !submitLock.current && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Uji Ulang Kerentanan #{findingId}</DialogTitle>
          <DialogDescription className="break-words">
            {findingName ? <span className="mb-2 block">{findingName}</span> : null}
            Agent uji ulang akan membaca bukti asli dan batasan tes, lalu menjalankan verifikasi tertarget di sesi
            terpisah. Setelah uji ulang selesai dan perbaikan terkonfirmasi, status kerentanan otomatis berubah
            menjadi "Sudah Diperbaiki", kesimpulan lain tetap mempertahankan status aslinya.
          </DialogDescription>
        </DialogHeader>
        <FieldGroup>
          <Field data-disabled={submitting}>
            <FieldLabel htmlFor={notesId}>Catatan Tambahan (opsional)</FieldLabel>
            <Textarea
              id={notesId}
              value={notes}
              maxLength={4000}
              rows={4}
              disabled={submitting}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="Contoh: memakai akun tes asli untuk memverifikasi endpoint asli; versi perbaikan v2."
            />
            <FieldDescription>Bisa menambahkan versi perbaikan, kondisi tes, atau batasan kali ini.</FieldDescription>
          </Field>
        </FieldGroup>
        <DialogFooter>
          <Button variant="outline" disabled={submitting} onClick={onClose}>
            Batal
          </Button>
          <Button disabled={submitting} onClick={() => void start()}>
            {submitting ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}
            {submitting ? "Membuat..." : "Mulai Uji Ulang"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
