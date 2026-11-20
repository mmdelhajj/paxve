package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Recruitment and appraisals (app "hr", permission module "talent").
//
// Recruitment: job positions (published on the public careers page /#/careers/<slug>), applicants from the careers
// form (CV kept as an attachment) or typed in, a pipeline new â†’ screening â†’ interview â†’ offer â†’ hired / refused,
// interviews with feedback and ratings, the offer (sent by WhatsApp) and the hire, which creates the employee record
// (payroll uses it like any other employee) and copies the CV to it.
//
// Appraisals: review cycles over a period; opening a cycle gives every active employee an appraisal with goals
// (weights, self and manager scores 1-5). Employees score themselves from the self-service screen, the reviewer
// scores and comments, closing the cycle fixes the final scores. Report: scores per employee and the spread.

var recruitStages = []string{"new", "screening", "interview", "offer", "hired", "refused"}

// ======================= jobs =======================

const jobPosCols = `j.id, j.title, j.department, j.location, j.employment, j.description, j.requirements, j.salary_range, j.vacancies, j.is_published,
	j.status, j.created_at, j.updated_at,
	(SELECT count(*) FROM job_applicants a WHERE a.job_id = j.id) AS applicants,
	(SELECT count(*) FROM job_applicants a WHERE a.job_id = j.id AND a.stage NOT IN ('hired', 'refused')) AS in_progress,
	(SELECT count(*) FROM job_applicants a WHERE a.job_id = j.id AND a.stage = 'hired') AS hired`

func (s *Server) talentJobs(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+jobPosCols+` FROM job_positions j WHERE j.company_id = $1 ORDER BY j.status, j.created_at DESC`, r.PathValue("cid"))
	})
}

type jobPosIn struct {
	Title        *string `json:"title"`
	Department   *string `json:"department"`
	Location     *string `json:"location"`
	Employment   *string `json:"employment"`
	Description  *string `json:"description"`
	Requirements *string `json:"requirements"`
	SalaryRange  *string `json:"salary_range"`
	Vacancies    *int    `json:"vacancies"`
	IsPublished  *bool   `json:"is_published"`
	Status       *string `json:"status"`
}

// POST /hr/jobs and PATCH /hr/jobs/{id}
func (s *Server) talentSaveJob(w http.ResponseWriter, r *http.Request) {
	var in jobPosIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	if (creating && strings.TrimSpace(deref(in.Title)) == "") || (in.Title != nil && (strings.TrimSpace(*in.Title) == "" || len(*in.Title) > 160)) ||
		(in.Employment != nil && !contains([]string{"full_time", "part_time", "contract", "internship", "temporary"}, *in.Employment)) ||
		(in.Status != nil && *in.Status != "open" && *in.Status != "closed") || (in.Vacancies != nil && (*in.Vacancies < 1 || *in.Vacancies > 1000)) ||
		len(deref(in.Department)) > 80 || len(deref(in.Location)) > 120 || len(deref(in.Description)) > 8000 || len(deref(in.Requirements)) > 4000 ||
		len(deref(in.SalaryRange)) > 80 || (!creating && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if creating {
			if err := tx.QueryRow(ctx, `INSERT INTO job_positions (company_id, title) VALUES ($1, trim($2)) RETURNING id::text`, cid, *in.Title).Scan(&id); err != nil {
				return nil, err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE job_positions SET title = coalesce(trim($3), title), department = coalesce(trim($4), department),
			location = coalesce(trim($5), location), employment = coalesce($6, employment), description = coalesce($7, description),
			requirements = coalesce($8, requirements), salary_range = coalesce(trim($9), salary_range), vacancies = coalesce($10, vacancies),
			is_published = coalesce($11, is_published), status = coalesce($12, status), updated_at = now() WHERE id = $1 AND company_id = $2`,
			id, cid, in.Title, in.Department, in.Location, in.Employment, in.Description, in.Requirements, in.SalaryRange, in.Vacancies, in.IsPublished, in.Status)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT `+jobPosCols+` FROM job_positions j WHERE j.id = $1`, id)
	})
}

// ======================= applicants =======================

const applicantCols = `a.id, a.number, a.job_id, j.title AS job_title, a.name, a.email, a.phone, a.source, a.stage, a.rating, a.cover_letter, a.expected_salary,
	a.notes, a.offer_salary::text AS offer_salary, a.offer_currency, a.offer_start, a.offer_sent_at, a.hired_employee_id, e.code AS employee_code,
	a.refuse_reason, a.created_at, a.updated_at,
	(SELECT count(*) FROM attachments f WHERE f.company_id = a.company_id AND f.entity_type = 'applicant' AND f.entity_id = a.id AND f.deleted_at IS NULL) AS files,
	(SELECT min(i.scheduled_at) FROM job_interviews i WHERE i.applicant_id = a.id AND i.status = 'planned' AND i.scheduled_at > now() - interval '2 hours') AS next_interview,
	(SELECT round(avg(i.rating), 1)::text FROM job_interviews i WHERE i.applicant_id = a.id AND i.rating IS NOT NULL) AS interview_rating`

const applicantFrom = ` FROM job_applicants a LEFT JOIN job_positions j ON j.id = a.job_id LEFT JOIN employees e ON e.id = a.hired_employee_id`

// GET /hr/applicants?job_id=&stage=&q=
func (s *Server) talentApplicants(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	job, stage, search := q.Get("job_id"), q.Get("stage"), strings.TrimSpace(q.Get("q"))
	if (job != "" && !isUUID(job)) || (stage != "" && !contains(recruitStages, stage)) || len(search) > 80 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+applicantCols+applicantFrom+` WHERE a.company_id = $1 AND ($2 = '' OR a.job_id = nullif($2, '')::uuid)
			AND ($3 = '' OR a.stage = $3) AND ($4 = '' OR a.name ILIKE '%' || $4 || '%' OR a.email ILIKE '%' || $4 || '%' OR a.phone LIKE '%' || $4 || '%')
			ORDER BY a.created_at DESC LIMIT 2000`, r.PathValue("cid"), job, stage, search)
	})
}

type applicantIn struct {
	JobID          *string `json:"job_id"`
	Name           *string `json:"name"`
	Email          *string `json:"email"`
	Phone          *string `json:"phone"`
	Source         *string `json:"source"`
	Stage          *string `json:"stage"`
	Rating         *int    `json:"rating"`
	CoverLetter    *string `json:"cover_letter"`
	ExpectedSalary *string `json:"expected_salary"`
	Notes          *string `json:"notes"`
	OfferSalary    *string `json:"offer_salary"`
	OfferCurrency  *string `json:"offer_currency"`
	OfferStart     *string `json:"offer_start"`
	RefuseReason   *string `json:"refuse_reason"`
}

func (in *applicantIn) valid(creating bool) bool {
	return !(creating && strings.TrimSpace(deref(in.Name)) == "") && !(in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 160)) &&
		optUUID(in.JobID) && len(deref(in.Email)) <= 200 && len(deref(in.Phone)) <= 40 &&
		(in.Source == nil || contains([]string{"careers", "manual", "referral", "other"}, *in.Source)) &&
		(in.Stage == nil || (contains(recruitStages, *in.Stage) && *in.Stage != "hired")) && (in.Rating == nil || (*in.Rating >= 0 && *in.Rating <= 5)) &&
		len(deref(in.CoverLetter)) <= 8000 && len(deref(in.ExpectedSalary)) <= 60 && len(deref(in.Notes)) <= 4000 &&
		(in.OfferSalary == nil || *in.OfferSalary == "" || okMoney(*in.OfferSalary)) &&
		(in.OfferCurrency == nil || *in.OfferCurrency == "" || currencyRe.MatchString(*in.OfferCurrency)) &&
		(in.OfferStart == nil || *in.OfferStart == "" || validDate(*in.OfferStart)) && len(deref(in.RefuseReason)) <= 500
}

func checkJob(ctx context.Context, tx pgx.Tx, cid, id string) error {
	if id == "" {
		return nil
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM job_positions WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return badRequest("invalid_job")
	}
	return nil
}

// POST /hr/applicants and PATCH /hr/applicants/{id}
func (s *Server) talentSaveApplicant(w http.ResponseWriter, r *http.Request) {
	var in applicantIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	if !in.valid(creating) || (!creating && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if err := checkJob(ctx, tx, cid, deref(in.JobID)); err != nil {
			return nil, err
		}
		if creating {
			var n int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'applicant')`, cid).Scan(&n); err != nil {
				return nil, err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO job_applicants (company_id, number, name) VALUES ($1, $2, trim($3)) RETURNING id::text`, cid, n, *in.Name).Scan(&id); err != nil {
				return nil, err
			}
		} else {
			var stage string
			if err := tx.QueryRow(ctx, `SELECT stage FROM job_applicants WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&stage); err != nil {
				if err == pgx.ErrNoRows {
					return nil, notFound()
				}
				return nil, err
			}
			if stage == "hired" && in.Stage != nil {
				return nil, &apiError{status: http.StatusConflict, code: "already_hired"}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE job_applicants SET job_id = CASE WHEN $3::text IS NULL THEN job_id ELSE nullif($3, '')::uuid END,
			name = coalesce(trim($4), name), email = coalesce(lower(trim($5)), email), phone = coalesce(trim($6), phone), source = coalesce($7, source),
			stage = coalesce($8, stage), rating = coalesce($9, rating), cover_letter = coalesce($10, cover_letter), expected_salary = coalesce(trim($11), expected_salary),
			notes = coalesce($12, notes), offer_salary = CASE WHEN $13::text IS NULL THEN offer_salary ELSE nullif($13, '')::numeric END,
			offer_currency = CASE WHEN $14::text IS NULL THEN offer_currency ELSE nullif($14, '')::char(3) END,
			offer_start = CASE WHEN $15::text IS NULL THEN offer_start ELSE nullif($15, '')::date END, refuse_reason = coalesce(trim($16), refuse_reason),
			updated_at = now() WHERE id = $1 AND company_id = $2`, id, cid, in.JobID, in.Name, in.Email, in.Phone, in.Source, in.Stage, in.Rating, in.CoverLetter,
			in.ExpectedSalary, in.Notes, in.OfferSalary, in.OfferCurrency, in.OfferStart, in.RefuseReason); err != nil {
			return nil, err
		}
		return loadApplicant(ctx, tx, cid, id)
	})
}

func loadApplicant(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	a, err := queryJSONOne(ctx, tx, `SELECT `+applicantCols+applicantFrom+` WHERE a.id = $1 AND a.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	iv, err := queryJSON(ctx, tx, `SELECT i.id, i.scheduled_at, i.duration_min, i.interviewer_id, u.full_name AS interviewer_name, i.kind, i.location, i.status,
		i.rating, i.feedback, i.created_at FROM job_interviews i LEFT JOIN users u ON u.id = i.interviewer_id WHERE i.applicant_id = $1 ORDER BY i.scheduled_at`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"applicant": a, "interviews": iv}, nil
}

func (s *Server) talentGetApplicant(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadApplicant(ctx, tx, cid, id)
	})
}

// POST /hr/applicants/{id}/{action}: offer {salary, currency, start} | hire {salary, currency, start, position} | refuse {reason}
func (s *Server) talentApplicantAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Salary   string `json:"salary"`
		Currency string `json:"currency"`
		Start    string `json:"start"`
		Position string `json:"position"`
		Reason   string `json:"reason"`
		Notify   *bool  `json:"notify"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id, action := r.PathValue("cid"), r.PathValue("id"), r.PathValue("action")
	if !isUUID(id) || !contains([]string{"offer", "hire", "refuse"}, action) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if (in.Salary != "" && !okMoney(in.Salary)) || (in.Currency != "" && !currencyRe.MatchString(in.Currency)) || (in.Start != "" && !validDate(in.Start)) ||
		len(in.Position) > 120 || len(in.Reason) > 500 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if action == "hire" && !can(r, "payroll.create") {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var stage, name, phone, email string
		var jobTitle *string
		var offerSal, offerCur, offerStart *string
		if err := tx.QueryRow(ctx, `SELECT a.stage, a.name, a.phone, a.email, j.title, a.offer_salary::text, a.offer_currency::text, a.offer_start::text
			FROM job_applicants a LEFT JOIN job_positions j ON j.id = a.job_id WHERE a.id = $1 AND a.company_id = $2 FOR UPDATE OF a`, id, cid).
			Scan(&stage, &name, &phone, &email, &jobTitle, &offerSal, &offerCur, &offerStart); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		if stage == "hired" {
			return nil, &apiError{status: http.StatusConflict, code: "already_hired"}
		}
		pick := func(given string, stored *string, def string) string {
			if given != "" {
				return given
			}
			if stored != nil && *stored != "" {
				return *stored
			}
			return def
		}
		salary, cur, start := pick(in.Salary, offerSal, "0"), pick(in.Currency, offerCur, base), pick(in.Start, offerStart, today())
		switch action {
		case "refuse":
			if _, err := tx.Exec(ctx, `UPDATE job_applicants SET stage = 'refused', refuse_reason = coalesce(nullif(trim($2), ''), refuse_reason), updated_at = now() WHERE id = $1`,
				id, in.Reason); err != nil {
				return nil, err
			}
		case "offer":
			if _, err := tx.Exec(ctx, `UPDATE job_applicants SET stage = 'offer', offer_salary = $2::numeric, offer_currency = $3, offer_start = $4::date,
				offer_sent_at = now(), updated_at = now() WHERE id = $1`, id, salary, cur, start); err != nil {
				return nil, err
			}
			if in.Notify == nil || *in.Notify {
				var company string
				var msgApp bool
				if err := tx.QueryRow(ctx, `SELECT coalesce(nullif(name_ar, ''), name_en), 'msg' = ANY (apps) FROM companies WHERE id = $1`, cid).Scan(&company, &msgApp); err != nil {
					return nil, err
				}
				body := growthText(ctx, tx, cid, "offer", company, name, deref(jobTitle))
				if msgApp && phone != "" {
					if _, _, err := queueOut(ctx, tx, cid, outMessage{Channel: "whatsapp", Phone: phone, Body: body, Event: "recruit_offer"}); err != nil {
						return nil, err
					}
				} else if email != "" {
					if _, _, err := queueOut(ctx, tx, cid, outMessage{Channel: "email", Email: email, Subject: company, Body: body, Event: "recruit_offer"}); err != nil {
						return nil, err
					}
				}
			}
		case "hire":
			pos := strings.TrimSpace(in.Position)
			if pos == "" {
				pos = deref(jobTitle)
			}
			var n int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'employee')`, cid).Scan(&n); err != nil {
				return nil, err
			}
			code := "E" + strconv.FormatInt(n, 10)
			for i := 0; i < 50; i++ {
				var taken bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM employees WHERE company_id = $1 AND code = $2)`, cid, code).Scan(&taken); err != nil {
					return nil, err
				}
				if !taken {
					break
				}
				if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'employee')`, cid).Scan(&n); err != nil {
					return nil, err
				}
				code = "E" + strconv.FormatInt(n, 10)
			}
			var emp string
			if err := tx.QueryRow(ctx, `INSERT INTO employees (company_id, code, name, phone, position, hire_date, currency, salary, notes)
				VALUES ($1, $2, $3, $4, $5, $6::date, $7, $8::numeric, $9) RETURNING id::text`,
				cid, code, name, phone, pos, start, cur, salary, fmt.Sprintf("Hired from applicant #%s", id[:8])).Scan(&emp); err != nil {
				return nil, err
			}
			// the CV and other files follow the person
			if _, err := tx.Exec(ctx, `INSERT INTO attachments (company_id, entity_type, entity_id, filename, mime, size, data, note)
				SELECT company_id, 'employee', $2, filename, mime, size, data, 'Recruitment' FROM attachments
				WHERE company_id = $3 AND entity_type = 'applicant' AND entity_id = $1 AND deleted_at IS NULL`, id, emp, cid); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE job_applicants SET stage = 'hired', hired_employee_id = $2, offer_salary = $3::numeric, offer_currency = $4,
				offer_start = $5::date, updated_at = now() WHERE id = $1`, id, emp, salary, cur, start); err != nil {
				return nil, err
			}
			// a position whose vacancies are filled closes
			if _, err := tx.Exec(ctx, `UPDATE job_positions j SET status = 'closed', is_published = false, updated_at = now()
				WHERE j.id = (SELECT job_id FROM job_applicants WHERE id = $1) AND j.status = 'open'
				  AND (SELECT count(*) FROM job_applicants a WHERE a.job_id = j.id AND a.stage = 'hired') >= j.vacancies`, id); err != nil {
				return nil, err
			}
		}
		return loadApplicant(ctx, tx, cid, id)
	})
}

// ======================= interviews =======================

type interviewIn struct {
	ApplicantID   *string `json:"applicant_id"`
	ScheduledAt   *string `json:"scheduled_at"`
	DurationMin   *int    `json:"duration_min"`
	InterviewerID *string `json:"interviewer_id"`
	Kind          *string `json:"kind"`
	Location      *string `json:"location"`
	Status        *string `json:"status"`
	Rating        *int    `json:"rating"`
	Feedback      *string `json:"feedback"`
}

// POST /hr/interviews and PATCH /hr/interviews/{id}
func (s *Server) talentSaveInterview(w http.ResponseWriter, r *http.Request) {
	var in interviewIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	var at *time.Time
	if in.ScheduledAt != nil {
		t, err := time.Parse(time.RFC3339, *in.ScheduledAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		at = &t
	}
	if (creating && (in.ApplicantID == nil || !isUUID(*in.ApplicantID) || at == nil)) || (!creating && (!isUUID(id) || in.ApplicantID != nil)) ||
		!optUUID(in.InterviewerID) || (in.DurationMin != nil && (*in.DurationMin < 5 || *in.DurationMin > 600)) ||
		(in.Kind != nil && !contains([]string{"phone", "video", "onsite"}, *in.Kind)) || (in.Status != nil && !contains([]string{"planned", "done", "cancelled"}, *in.Status)) ||
		(in.Rating != nil && (*in.Rating < 1 || *in.Rating > 5)) || len(deref(in.Location)) > 200 || len(deref(in.Feedback)) > 4000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if u := deref(in.InterviewerID); u != "" {
			if err := checkCompanyUser(ctx, tx, cid, u); err != nil {
				return nil, err
			}
		}
		var applicant string
		if creating {
			var stage string
			if err := tx.QueryRow(ctx, `SELECT stage FROM job_applicants WHERE id = $1 AND company_id = $2`, *in.ApplicantID, cid).Scan(&stage); err != nil {
				if err == pgx.ErrNoRows {
					return nil, badRequest("invalid_applicant")
				}
				return nil, err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO job_interviews (company_id, applicant_id, scheduled_at) VALUES ($1, $2, $3) RETURNING id::text, applicant_id::text`,
				cid, *in.ApplicantID, *at).Scan(&id, &applicant); err != nil {
				return nil, err
			}
			// scheduling an interview moves an early applicant to the interview column
			if _, err := tx.Exec(ctx, `UPDATE job_applicants SET stage = 'interview', updated_at = now() WHERE id = $1 AND stage IN ('new', 'screening')`, applicant); err != nil {
				return nil, err
			}
		} else if err := tx.QueryRow(ctx, `SELECT applicant_id::text FROM job_interviews WHERE id = $1 AND company_id = $2`, id, cid).Scan(&applicant); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE job_interviews SET scheduled_at = coalesce($3, scheduled_at), duration_min = coalesce($4, duration_min),
			interviewer_id = CASE WHEN $5::text IS NULL THEN interviewer_id ELSE nullif($5, '')::uuid END, kind = coalesce($6, kind),
			location = coalesce(trim($7), location), status = coalesce($8, status), rating = coalesce($9, rating), feedback = coalesce($10, feedback)
			WHERE id = $1 AND company_id = $2`, id, cid, at, in.DurationMin, in.InterviewerID, in.Kind, in.Location, in.Status, in.Rating, in.Feedback); err != nil {
			return nil, err
		}
		if iv := deref(in.InterviewerID); iv != "" && creating {
			var name string
			_ = tx.QueryRow(ctx, `SELECT name FROM job_applicants WHERE id = $1`, applicant).Scan(&name)
			if err := notifyUsers(ctx, tx, cid, []string{iv}, "recruit", "Interview: "+name, at.Format("2006-01-02 15:04"), "applicant", applicant); err != nil {
				return nil, err
			}
		}
		return loadApplicant(ctx, tx, cid, applicant)
	})
}

// GET /hr/recruitment-report: per position the pipeline, sources and the average days to hire.
func (s *Server) talentReport(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		jobs, err := queryJSON(ctx, tx, `SELECT j.id, j.title, j.status, j.vacancies,
			count(a.id) AS applicants, count(a.id) FILTER (WHERE a.stage = 'new') AS new, count(a.id) FILTER (WHERE a.stage = 'screening') AS screening,
			count(a.id) FILTER (WHERE a.stage = 'interview') AS interview, count(a.id) FILTER (WHERE a.stage = 'offer') AS offer,
			count(a.id) FILTER (WHERE a.stage = 'hired') AS hired, count(a.id) FILTER (WHERE a.stage = 'refused') AS refused,
			round(avg(extract(epoch FROM a.updated_at - a.created_at) / 86400) FILTER (WHERE a.stage = 'hired'), 1)::text AS days_to_hire
			FROM job_positions j LEFT JOIN job_applicants a ON a.job_id = j.id WHERE j.company_id = $1 GROUP BY j.id ORDER BY j.status, j.created_at DESC`, r.PathValue("cid"))
		if err != nil {
			return nil, err
		}
		sources, err := queryJSON(ctx, tx, `SELECT source, count(*) AS applicants, count(*) FILTER (WHERE stage = 'hired') AS hired
			FROM job_applicants WHERE company_id = $1 GROUP BY source ORDER BY 2 DESC`, r.PathValue("cid"))
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"jobs": jobs, "sources": sources}, nil
	})
}

// ======================= public careers page =======================

// GET /api/v1/careers/{slug}: the company and its published open positions.
func (s *Server) careersHome(w http.ResponseWriter, r *http.Request) {
	sc, cid, _, on, ok := s.publicScope(w, r, false)
	if !ok {
		return
	}
	if !on {
		writeErr(w, http.StatusNotFound, "careers_closed")
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		c, err := queryJSONOne(ctx, tx, `SELECT c.name_en, c.name_ar, c.logo, c.default_locale, s.careers_intro, s.color, s.enabled AND 'web' = ANY (c.apps) AS shop
			FROM companies c JOIN shop_settings s ON s.company_id = c.id WHERE c.id = $1`, cid)
		if err != nil {
			return nil, err
		}
		jobs, err := queryJSON(ctx, tx, `SELECT j.id, j.title, j.department, j.location, j.employment, j.description, j.requirements, j.salary_range, j.created_at
			FROM job_positions j WHERE j.company_id = $1 AND j.is_published AND j.status = 'open' ORDER BY j.created_at DESC`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"company": c, "jobs": jobs}, nil
	})
}

// POST /api/v1/careers/{slug}/jobs/{id}/apply {name, email, phone, cover_letter, expected_salary, cv: {filename, mime, data}, website}
func (s *Server) careersApply(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	var in struct {
		Name           string `json:"name"`
		Email          string `json:"email"`
		Phone          string `json:"phone"`
		CoverLetter    string `json:"cover_letter"`
		ExpectedSalary string `json:"expected_salary"`
		CV             *struct {
			Filename string `json:"filename"`
			Mime     string `json:"mime"`
			Data     string `json:"data"`
		} `json:"cv"`
		Website string `json:"website"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	job := r.PathValue("id")
	in.Name, in.Email, in.Phone = strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.Email)), strings.TrimSpace(in.Phone)
	if !isUUID(job) || in.Name == "" || len(in.Name) > 160 || (in.Email == "" && in.Phone == "") || (in.Email != "" && (!strings.Contains(in.Email, "@") || len(in.Email) > 200)) ||
		len(in.Phone) > 40 || len(in.CoverLetter) > 8000 || len(in.ExpectedSalary) > 60 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var cv []byte
	var cvMime, cvName string
	if in.CV != nil && in.CV.Data != "" {
		data := in.CV.Data
		if i := strings.Index(data, ";base64,"); strings.HasPrefix(data, "data:") && i > 0 {
			data = data[i+8:]
		}
		b, err := base64.StdEncoding.DecodeString(data)
		cvName = strings.TrimSpace(in.CV.Filename)
		if err != nil || len(b) == 0 || len(b) > 5<<20 || cvName == "" || len(cvName) > 200 {
			writeErr(w, http.StatusBadRequest, "bad_cv")
			return
		}
		cvMime = http.DetectContentType(b)
		lower := strings.ToLower(cvName)
		okType := cvMime == "application/pdf" || strings.HasPrefix(cvMime, "image/") && cvMime != "image/svg+xml" ||
			((strings.HasSuffix(lower, ".doc") || strings.HasSuffix(lower, ".docx")) && (cvMime == "application/zip" || cvMime == "application/octet-stream" || strings.HasPrefix(cvMime, "application/x-ole")))
		if !okType {
			writeErr(w, http.StatusBadRequest, "bad_cv")
			return
		}
		if strings.HasSuffix(lower, ".docx") {
			cvMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		} else if strings.HasSuffix(lower, ".doc") {
			cvMime = "application/msword"
		}
		cv = b
	}
	sc, cid, _, on, ok := s.publicScope(w, r, true)
	if !ok {
		return
	}
	if !on {
		writeErr(w, http.StatusNotFound, "careers_closed")
		return
	}
	if in.Website != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var title string
		if err := tx.QueryRow(ctx, `SELECT title FROM job_positions WHERE id = $1 AND company_id = $2 AND is_published AND status = 'open'`, job, cid).Scan(&title); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		// one application per person and position
		var dup bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM job_applicants WHERE company_id = $1 AND job_id = $2
			AND ((email <> '' AND email = $3) OR (phone <> '' AND right(regexp_replace(phone, '[^0-9]', '', 'g'), 7) = right(regexp_replace($4, '[^0-9]', '', 'g'), 7)))
			AND created_at > now() - interval '90 days')`, cid, job, in.Email, in.Phone).Scan(&dup); err != nil {
			return nil, err
		}
		if dup {
			return nil, &apiError{status: http.StatusConflict, code: "already_applied"}
		}
		if len(cv) > 0 {
			if err := checkStorageQuota(ctx, tx, cid, len(cv)); err != nil {
				return nil, err
			}
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'applicant')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO job_applicants (company_id, number, job_id, name, email, phone, source, cover_letter, expected_salary, ip)
			VALUES ($1, $2, $3, $4, $5, $6, 'careers', $7, $8, $9) RETURNING id::text`, cid, n, job, in.Name, in.Email, in.Phone, strings.TrimSpace(in.CoverLetter),
			strings.TrimSpace(in.ExpectedSalary), clientIP(r)).Scan(&id); err != nil {
			return nil, err
		}
		if len(cv) > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO attachments (company_id, entity_type, entity_id, filename, mime, size, data, note, created_by)
				VALUES ($1, 'applicant', $2, $3, $4, $5, $6, 'CV', NULL)`, cid, id, cvName, cvMime, len(cv), cv); err != nil {
				return nil, err
			}
		}
		if err := notifyStaff(ctx, tx, cid, "talent.view", "recruit", fmt.Sprintf("New application آ· %s", title), in.Name, "applicant", id); err != nil {
			return nil, err
		}
		var company string
		var msgApp bool
		if err := tx.QueryRow(ctx, `SELECT coalesce(nullif(name_ar, ''), name_en), 'msg' = ANY (apps) FROM companies WHERE id = $1`, cid).Scan(&company, &msgApp); err != nil {
			return nil, err
		}
		body := growthText(ctx, tx, cid, "applied", company, in.Name, title)
		if msgApp && in.Phone != "" {
			if _, _, err := queueOut(ctx, tx, cid, outMessage{Channel: "whatsapp", Phone: in.Phone, Body: body, Event: "recruit_applied"}); err != nil {
				return nil, err
			}
		}
		return map[string]any{"ok": true, "number": n}, nil
	})
}

// ======================= appraisals =======================

type goal struct {
	Title   string           `json:"title"`
	Weight  decimal.Decimal  `json:"weight"`
	Target  string           `json:"target"`
	Self    *decimal.Decimal `json:"self"`
	Manager *decimal.Decimal `json:"manager"`
	Note    string           `json:"note"`
}

func validGoals(gs []goal) bool {
	if len(gs) > 30 {
		return false
	}
	five := decimal.NewFromInt(5)
	for _, g := range gs {
		if strings.TrimSpace(g.Title) == "" || len(g.Title) > 200 || len(g.Target) > 300 || len(g.Note) > 1000 || g.Weight.IsNegative() || g.Weight.GreaterThan(hundred) {
			return false
		}
		for _, sc := range []*decimal.Decimal{g.Self, g.Manager} {
			if sc != nil && (sc.IsNegative() || sc.GreaterThan(five)) {
				return false
			}
		}
	}
	return true
}

// weighted average of one side's scores (nil when no goal is scored)
func goalScore(gs []goal, manager bool) *decimal.Decimal {
	sum, weights := decimal.Zero, decimal.Zero
	for _, g := range gs {
		sc := g.Self
		if manager {
			sc = g.Manager
		}
		if sc == nil {
			continue
		}
		w := g.Weight
		if !w.IsPositive() {
			w = decimal.NewFromInt(1)
		}
		sum, weights = sum.Add(sc.Mul(w)), weights.Add(w)
	}
	if !weights.IsPositive() {
		return nil
	}
	v := sum.Div(weights).Round(2)
	return &v
}

const cycleCols = `c.id, c.name, c.period_from, c.period_to, c.due_date, c.self_review, c.status, c.created_at,
	(SELECT count(*) FROM appraisals a WHERE a.cycle_id = c.id) AS appraisals,
	(SELECT count(*) FROM appraisals a WHERE a.cycle_id = c.id AND a.status IN ('reviewed', 'closed')) AS reviewed,
	(SELECT round(avg(coalesce(a.final_score, a.manager_score)), 2)::text FROM appraisals a WHERE a.cycle_id = c.id) AS avg_score`

func (s *Server) talentCycles(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+cycleCols+` FROM appraisal_cycles c WHERE c.company_id = $1 ORDER BY c.period_from DESC`, r.PathValue("cid"))
	})
}

// POST /hr/appraisal-cycles and PATCH /hr/appraisal-cycles/{id} {name, period_from, period_to, due_date, self_review}
func (s *Server) talentSaveCycle(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       *string `json:"name"`
		PeriodFrom *string `json:"period_from"`
		PeriodTo   *string `json:"period_to"`
		DueDate    *string `json:"due_date"`
		SelfReview *bool   `json:"self_review"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	if (creating && (strings.TrimSpace(deref(in.Name)) == "" || in.PeriodFrom == nil || in.PeriodTo == nil)) || (in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 120)) ||
		(in.PeriodFrom != nil && !validDate(*in.PeriodFrom)) || (in.PeriodTo != nil && !validDate(*in.PeriodTo)) || (in.DueDate != nil && *in.DueDate != "" && !validDate(*in.DueDate)) ||
		(!creating && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if creating {
			if err := tx.QueryRow(ctx, `INSERT INTO appraisal_cycles (company_id, name, period_from, period_to) VALUES ($1, trim($2), $3::date, $4::date) RETURNING id::text`,
				cid, *in.Name, *in.PeriodFrom, *in.PeriodTo).Scan(&id); err != nil {
				return nil, err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE appraisal_cycles SET name = coalesce(trim($3), name), period_from = coalesce($4::date, period_from),
			period_to = coalesce($5::date, period_to), due_date = CASE WHEN $6::text IS NULL THEN due_date ELSE nullif($6, '')::date END,
			self_review = coalesce($7, self_review) WHERE id = $1 AND company_id = $2 AND status <> 'closed'`,
			id, cid, in.Name, in.PeriodFrom, in.PeriodTo, in.DueDate, in.SelfReview)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		return queryJSONOne(ctx, tx, `SELECT `+cycleCols+` FROM appraisal_cycles c WHERE c.id = $1`, id)
	})
}

// POST /hr/appraisal-cycles/{id}/{action}: open {reviewer_id, goals} (one appraisal per active employee) | close
func (s *Server) talentCycleAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ReviewerID string `json:"reviewer_id"`
		Goals      []goal `json:"goals"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id, action := r.PathValue("cid"), r.PathValue("id"), r.PathValue("action")
	if !isUUID(id) || (action != "open" && action != "close") {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if (in.ReviewerID != "" && !isUUID(in.ReviewerID)) || !validGoals(in.Goals) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var st string
		if err := tx.QueryRow(ctx, `SELECT status FROM appraisal_cycles WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		switch action {
		case "open":
			if st == "closed" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			if in.ReviewerID != "" {
				if err := checkCompanyUser(ctx, tx, cid, in.ReviewerID); err != nil {
					return nil, err
				}
			}
			goals := in.Goals
			if goals == nil {
				goals = []goal{}
			}
			gj, _ := json.Marshal(goals)
			if _, err := tx.Exec(ctx, `INSERT INTO appraisals (company_id, cycle_id, employee_id, reviewer_id, goals)
				SELECT $1, $2, e.id, coalesce(nullif($3, '')::uuid, app_user()), $4::jsonb FROM employees e
				WHERE e.company_id = $1 AND e.status = 'active' ON CONFLICT (cycle_id, employee_id) DO NOTHING`, cid, id, in.ReviewerID, string(gj)); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE appraisal_cycles SET status = 'open' WHERE id = $1`, id); err != nil {
				return nil, err
			}
			// employees with a user account are told to fill their self-review
			rows, err := tx.Query(ctx, `SELECT e.user_id::text FROM appraisals a JOIN employees e ON e.id = a.employee_id JOIN appraisal_cycles c ON c.id = a.cycle_id
				WHERE a.cycle_id = $1 AND e.user_id IS NOT NULL AND c.self_review AND a.status = 'pending'`, id)
			if err != nil {
				return nil, err
			}
			users, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return nil, err
			}
			var name string
			_ = tx.QueryRow(ctx, `SELECT name FROM appraisal_cycles WHERE id = $1`, id).Scan(&name)
			if err := notifyUsers(ctx, tx, cid, users, "appraisal", "Appraisal: "+name, "Fill in your self-review", "", ""); err != nil {
				return nil, err
			}
		case "close":
			if st != "open" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			if _, err := tx.Exec(ctx, `UPDATE appraisals SET final_score = coalesce(final_score, manager_score, self_score), status = 'closed', closed_at = now(), updated_at = now()
				WHERE cycle_id = $1`, id); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE appraisal_cycles SET status = 'closed' WHERE id = $1`, id); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+cycleCols+` FROM appraisal_cycles c WHERE c.id = $1`, id)
	})
}

const appraisalCols = `a.id, a.cycle_id, c.name AS cycle_name, c.period_from, c.period_to, c.due_date, c.status AS cycle_status, c.self_review,
	a.employee_id, e.code AS employee_code, e.name AS employee_name, e.position, a.reviewer_id, u.full_name AS reviewer_name, a.goals,
	a.self_comment, a.manager_comment, a.strengths, a.improvements, a.self_score::text AS self_score, a.manager_score::text AS manager_score,
	a.final_score::text AS final_score, a.status, a.self_at, a.reviewed_at, a.closed_at, a.updated_at`

const appraisalFrom = ` FROM appraisals a JOIN appraisal_cycles c ON c.id = a.cycle_id JOIN employees e ON e.id = a.employee_id LEFT JOIN users u ON u.id = a.reviewer_id`

// GET /hr/appraisals?cycle_id=&employee_id=
func (s *Server) talentAppraisals(w http.ResponseWriter, r *http.Request) {
	cy, emp := r.URL.Query().Get("cycle_id"), r.URL.Query().Get("employee_id")
	if (cy != "" && !isUUID(cy)) || (emp != "" && !isUUID(emp)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+appraisalCols+appraisalFrom+` WHERE a.company_id = $1 AND ($2 = '' OR a.cycle_id = nullif($2, '')::uuid)
			AND ($3 = '' OR a.employee_id = nullif($3, '')::uuid) ORDER BY c.period_from DESC, e.name LIMIT 2000`, r.PathValue("cid"), cy, emp)
	})
}

func (s *Server) talentGetAppraisal(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT `+appraisalCols+appraisalFrom+` WHERE a.id = $1 AND a.company_id = $2`, id, cid)
	})
}

// PATCH /hr/appraisals/{id} {goals, reviewer_id, manager_comment, strengths, improvements, final_score, submit}: the reviewer's side.
func (s *Server) talentReviewAppraisal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Goals          *[]goal `json:"goals"`
		ReviewerID     *string `json:"reviewer_id"`
		ManagerComment *string `json:"manager_comment"`
		Strengths      *string `json:"strengths"`
		Improvements   *string `json:"improvements"`
		FinalScore     *string `json:"final_score"`
		Submit         bool    `json:"submit"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || (in.Goals != nil && !validGoals(*in.Goals)) || !optUUID(in.ReviewerID) || len(deref(in.ManagerComment)) > 4000 ||
		len(deref(in.Strengths)) > 2000 || len(deref(in.Improvements)) > 2000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.FinalScore != nil && *in.FinalScore != "" {
		if v, ok := money(*in.FinalScore); !ok || v.GreaterThan(decimal.NewFromInt(5)) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status, cycle string
		var gj []byte
		if err := tx.QueryRow(ctx, `SELECT a.status, c.status, a.goals FROM appraisals a JOIN appraisal_cycles c ON c.id = a.cycle_id
			WHERE a.id = $1 AND a.company_id = $2 FOR UPDATE OF a`, id, cid).Scan(&status, &cycle, &gj); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		if status == "closed" || cycle == "closed" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if u := deref(in.ReviewerID); u != "" {
			if err := checkCompanyUser(ctx, tx, cid, u); err != nil {
				return nil, err
			}
		}
		var goals []goal
		_ = json.Unmarshal(gj, &goals)
		if in.Goals != nil {
			// the reviewer sets the goals and the manager scores; the employee's own scores are kept
			old := map[string]*decimal.Decimal{}
			for _, g := range goals {
				old[strings.TrimSpace(g.Title)] = g.Self
			}
			goals = *in.Goals
			for i := range goals {
				goals[i].Self = old[strings.TrimSpace(goals[i].Title)]
			}
		}
		ms := goalScore(goals, true)
		var msS *string
		if ms != nil {
			v := ms.String()
			msS = &v
		}
		newGoals, _ := json.Marshal(goals)
		newStatus := status
		if in.Submit {
			newStatus = "reviewed"
		}
		if _, err := tx.Exec(ctx, `UPDATE appraisals SET goals = $3::jsonb, reviewer_id = CASE WHEN $4::text IS NULL THEN reviewer_id ELSE nullif($4, '')::uuid END,
			manager_comment = coalesce($5, manager_comment), strengths = coalesce($6, strengths), improvements = coalesce($7, improvements),
			manager_score = $8::numeric, final_score = CASE WHEN $9::text IS NULL THEN final_score ELSE nullif($9, '')::numeric END,
			status = $10, reviewed_at = CASE WHEN $10 = 'reviewed' AND status <> 'reviewed' THEN now() ELSE reviewed_at END, updated_at = now()
			WHERE id = $1 AND company_id = $2`, id, cid, string(newGoals), in.ReviewerID, in.ManagerComment, in.Strengths, in.Improvements, msS, in.FinalScore, newStatus); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+appraisalCols+appraisalFrom+` WHERE a.id = $1`, id)
	})
}

// GET /hr/appraisal-report?cycle_id=: scores per employee, the average and the spread (1-5).
func (s *Server) talentAppraisalReport(w http.ResponseWriter, r *http.Request) {
	cy := r.URL.Query().Get("cycle_id")
	if cy != "" && !isUUID(cy) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := queryJSON(ctx, tx, `SELECT a.employee_id, e.code, e.name, e.position, c.name AS cycle_name, a.self_score::text AS self_score,
			a.manager_score::text AS manager_score, coalesce(a.final_score, a.manager_score)::text AS score, a.status
			FROM appraisals a JOIN employees e ON e.id = a.employee_id JOIN appraisal_cycles c ON c.id = a.cycle_id
			WHERE a.company_id = $1 AND ($2 = '' OR a.cycle_id = nullif($2, '')::uuid) ORDER BY coalesce(a.final_score, a.manager_score) DESC NULLS LAST, e.name`,
			r.PathValue("cid"), cy)
		if err != nil {
			return nil, err
		}
		sum, err := queryJSONOne(ctx, tx, `SELECT count(*) AS appraisals, round(avg(coalesce(final_score, manager_score)), 2)::text AS average,
			count(*) FILTER (WHERE coalesce(final_score, manager_score) >= 4.5) AS outstanding,
			count(*) FILTER (WHERE coalesce(final_score, manager_score) >= 3.5 AND coalesce(final_score, manager_score) < 4.5) AS strong,
			count(*) FILTER (WHERE coalesce(final_score, manager_score) >= 2.5 AND coalesce(final_score, manager_score) < 3.5) AS meets,
			count(*) FILTER (WHERE coalesce(final_score, manager_score) < 2.5) AS below,
			count(*) FILTER (WHERE coalesce(final_score, manager_score) IS NULL) AS pending
			FROM appraisals WHERE company_id = $1 AND ($2 = '' OR cycle_id = nullif($2, '')::uuid)`, r.PathValue("cid"), cy)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"rows": rows, "summary": sum}, nil
	})
}

// ---------- self-review (the employee of the signed-in user) ----------

// GET /self/appraisals
func (s *Server) selfAppraisals(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		emp, err := selfEmployee(ctx, tx, cid, principal(r).ID)
		if err != nil {
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT `+appraisalCols+appraisalFrom+` WHERE a.employee_id = $1 AND c.status <> 'draft' ORDER BY c.period_from DESC`, emp)
	})
}

// PATCH /self/appraisals/{id} {goals: [{title, self}], self_comment, submit}
func (s *Server) selfReviewAppraisal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Goals       []goal  `json:"goals"`
		SelfComment *string `json:"self_comment"`
		Submit      bool    `json:"submit"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || len(in.Goals) > 30 || len(deref(in.SelfComment)) > 4000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	five := decimal.NewFromInt(5)
	for _, g := range in.Goals {
		if g.Self != nil && (g.Self.IsNegative() || g.Self.GreaterThan(five)) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		emp, err := selfEmployee(ctx, tx, cid, principal(r).ID)
		if err != nil {
			return nil, err
		}
		var status, cycle string
		var selfReview bool
		var gj []byte
		var reviewer *string
		if err := tx.QueryRow(ctx, `SELECT a.status, c.status, c.self_review, a.goals, a.reviewer_id::text FROM appraisals a JOIN appraisal_cycles c ON c.id = a.cycle_id
			WHERE a.id = $1 AND a.employee_id = $2 FOR UPDATE OF a`, id, emp).Scan(&status, &cycle, &selfReview, &gj, &reviewer); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		if cycle != "open" || !selfReview || (status != "pending" && status != "self_done") {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		var goals []goal
		_ = json.Unmarshal(gj, &goals)
		mine := map[string]*decimal.Decimal{}
		for _, g := range in.Goals {
			mine[strings.TrimSpace(g.Title)] = g.Self
		}
		for i := range goals {
			if v, ok := mine[strings.TrimSpace(goals[i].Title)]; ok {
				goals[i].Self = v
			}
		}
		ss := goalScore(goals, false)
		var ssS *string
		if ss != nil {
			v := ss.String()
			ssS = &v
		}
		newGoals, _ := json.Marshal(goals)
		newStatus := status
		if in.Submit {
			newStatus = "self_done"
		}
		if _, err := tx.Exec(ctx, `UPDATE appraisals SET goals = $2::jsonb, self_comment = coalesce($3, self_comment), self_score = $4::numeric, status = $5,
			self_at = CASE WHEN $5 = 'self_done' THEN now() ELSE self_at END, updated_at = now() WHERE id = $1`, id, string(newGoals), in.SelfComment, ssS, newStatus); err != nil {
			return nil, err
		}
		if in.Submit && reviewer != nil {
			var name string
			_ = tx.QueryRow(ctx, `SELECT name FROM employees WHERE id = $1`, emp).Scan(&name)
			if err := notifyUsers(ctx, tx, cid, []string{*reviewer}, "appraisal", "Self-review done: "+name, "", "", ""); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+appraisalCols+appraisalFrom+` WHERE a.id = $1`, id)
	})
}
