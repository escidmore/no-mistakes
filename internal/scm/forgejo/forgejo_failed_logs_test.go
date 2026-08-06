package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestChecksPreserveProviderStateAndTargetLink(t *testing.T) {
	target := testBaseURL + "/" + testRepo + "/actions/runs/91/jobs/0"
	statuses := fmt.Sprintf(`[{"context":"CI / test (pull_request)","state":"failure","description":"boom","target_url":%q,"created_at":null,"updated_at":null}]`, target)
	host := newTestHost(&fakeRecorder{responses: []fakeResponse{{stdout: checksJSON("failure", "not_required", false, statuses, `[]`)}}})

	checks, err := host.GetChecks(context.Background(), testPR())
	if err != nil {
		t.Fatalf("GetChecks() error = %v", err)
	}
	if len(checks) != 1 || checks[0].State != "failure" || checks[0].Link != target {
		t.Fatalf("GetChecks() = %+v, want provider failure state and exact target link", checks)
	}
}

func TestFetchFailedCheckLogsUsesCanonicalTargetAndExactIdentities(t *testing.T) {
	target := testBaseURL + "/" + testRepo + "/actions/runs/91/jobs/0"
	statuses := fmt.Sprintf(`[{"context":"CI / test (pull_request)","state":"failure","target_url":%q}]`, target)
	recorder := &fakeRecorder{responses: []fakeResponse{
		{stdout: fixture(t, "status-forgejo-16.json")},
		{stdout: checksJSON("failure", "not_required", false, statuses, `[]`)},
		{stdout: failedLogRunViewJSON(91, testHeadSHA, []string{`{"id":501,"run_id":91,"name":"test","status":"failure","log":"assertion failed"}`})},
	}}
	host := newTestHost(recorder)
	if err := host.Available(context.Background()); err != nil {
		t.Fatalf("Available() error = %v", err)
	}

	logs, err := host.FetchFailedCheckLogs(context.Background(), testPR(), "feature/forgejo", testHeadSHA, []string{"CI / test (pull_request)"})
	if err != nil {
		t.Fatalf("FetchFailedCheckLogs() error = %v", err)
	}
	if !strings.Contains(logs, "Forgejo Actions run 91, job test:") || !strings.Contains(logs, "assertion failed") {
		t.Fatalf("FetchFailedCheckLogs() = %q, want identified failed-job log", logs)
	}
	want := []string{"run", "view", "--repo", testRepo, "91", "--log-failed", "--base-url", testBaseURL, "--token-env", "FORGEJO_TEST_TOKEN", "--json"}
	if got := recorder.calls[2].args; !reflect.DeepEqual(got, want) {
		t.Fatalf("run view args = %#v, want %#v", got, want)
	}
}

func TestFetchFailedCheckLogsDeduplicatesAndSortsRunAndJobIDs(t *testing.T) {
	target := func(runID, job int) string {
		return fmt.Sprintf("%s/%s/actions/runs/%d/jobs/%d", testBaseURL, testRepo, runID, job)
	}
	statuses := fmt.Sprintf(`[
		{"context":"CI / second (pull_request)","state":"failure","target_url":%q},
		{"context":"Lint / lint (pull_request)","state":"failure","target_url":%q},
		{"context":"CI / first (pull_request)","state":"failure","target_url":%q}
	]`, target(20, 1), target(10, 0), target(20, 0))
	recorder := &fakeRecorder{responses: []fakeResponse{
		{stdout: fixture(t, "status-forgejo-16.json")},
		{stdout: checksJSON("failure", "not_required", false, statuses, `[]`)},
		{stdout: failedLogRunViewJSON(10, testHeadSHA, []string{`{"id":100,"run_id":10,"name":"lint","status":"failure","log":"lint failed"}`})},
		{stdout: failedLogRunViewJSON(20, testHeadSHA, []string{
			`{"id":201,"run_id":20,"name":"test-second","status":"failure","log":"second failed"}`,
			`{"id":200,"run_id":20,"name":"test-first","status":"failure","log":"first failed"}`,
		})},
	}}
	host := newTestHost(recorder)
	if err := host.Available(context.Background()); err != nil {
		t.Fatalf("Available() error = %v", err)
	}

	logs, err := host.FetchFailedCheckLogs(context.Background(), testPR(), "feature/forgejo", testHeadSHA, []string{
		"CI / first (pull_request)", "CI / second (pull_request)", "Lint / lint (pull_request)",
	})
	if err != nil {
		t.Fatalf("FetchFailedCheckLogs() error = %v", err)
	}
	run10, run20 := strings.Index(logs, "run 10"), strings.Index(logs, "run 20")
	firstJob, secondJob := strings.Index(logs, "job test-first:"), strings.Index(logs, "job test-second:")
	if run10 < 0 || run20 < 0 || run10 > run20 || firstJob < 0 || secondJob < 0 || firstJob > secondJob {
		t.Fatalf("FetchFailedCheckLogs() = %q, want numeric run and job order", logs)
	}
	if len(recorder.calls) != 4 || recorder.calls[2].args[4] != "10" || recorder.calls[3].args[4] != "20" {
		t.Fatalf("run view calls = %#v, want one call each in 10,20 order", recorder.calls)
	}
}

func TestFetchFailedCheckLogsRejectsRunHeadAndJobMismatches(t *testing.T) {
	canonical := failedLogRunViewJSON(91, testHeadSHA, []string{`{"id":501,"run_id":91,"name":"test","status":"failure","log":"failed"}`})
	tests := []struct {
		name string
		view string
		want string
	}{
		{name: "wrong run", view: failedLogRunViewJSON(92, testHeadSHA, []string{`{"id":501,"run_id":92,"name":"test","status":"failure","log":"failed"}`}), want: "run 92, expected 91"},
		{name: "wrong head", view: failedLogRunViewJSON(91, strings.Repeat("b", 40), []string{`{"id":501,"run_id":91,"name":"test","status":"failure","log":"failed"}`}), want: "pull request head changed"},
		{name: "wrong job run", view: strings.Replace(canonical, `"run_id":91`, `"run_id":92`, 1), want: "job 501 for run 92"},
		{name: "empty job name", view: strings.Replace(canonical, `"name":"test"`, `"name":" "`, 1), want: "job 501 without a name"},
		{name: "duplicate job ID", view: failedLogRunViewJSON(91, testHeadSHA, []string{
			`{"id":501,"run_id":91,"name":"first","status":"failure","log":"failed"}`,
			`{"id":501,"run_id":91,"name":"second","status":"failure","log":"failed"}`,
		}), want: "duplicate job 501"},
		{name: "failed job omitted log", view: failedLogRunViewJSON(91, testHeadSHA, []string{`{"id":501,"run_id":91,"name":"test","status":"failure"}`}), want: "without a log"},
		{name: "unsupported next", view: strings.TrimSuffix(canonical, "}") + `,"next":["Job logs are unsupported"]}`, want: "did not provide requested failed logs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := testBaseURL + "/" + testRepo + "/actions/runs/91/jobs/0"
			statuses := fmt.Sprintf(`[{"context":"CI / test (pull_request)","state":"failure","target_url":%q}]`, target)
			recorder := &fakeRecorder{responses: []fakeResponse{
				{stdout: fixture(t, "status-forgejo-16.json")},
				{stdout: checksJSON("failure", "not_required", false, statuses, `[]`)},
				{stdout: tt.view},
			}}
			host := newTestHost(recorder)
			if err := host.Available(context.Background()); err != nil {
				t.Fatalf("Available() error = %v", err)
			}
			logs, err := host.FetchFailedCheckLogs(context.Background(), testPR(), "feature/forgejo", testHeadSHA, []string{"CI / test (pull_request)"})
			if logs != "" || err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("FetchFailedCheckLogs() = (%q, %v), want error containing %q", logs, err, tt.want)
			}
		})
	}
}

func TestFetchFailedCheckLogsUsesLiveCheckHeadForRunLookup(t *testing.T) {
	target := testBaseURL + "/" + testRepo + "/actions/runs/91/jobs/0"
	statuses := fmt.Sprintf(`[{"context":"CI / test (pull_request)","state":"failure","target_url":%q}]`, target)
	recorder := &fakeRecorder{responses: []fakeResponse{
		{stdout: fixture(t, "status-forgejo-16.json")},
		{stdout: checksJSON("failure", "not_required", false, statuses, `[]`)},
		{stdout: failedLogRunViewJSON(91, testHeadSHA, []string{`{"id":501,"run_id":91,"name":"test","status":"failure","log":"failed"}`})},
	}}
	host := newTestHost(recorder)
	if err := host.Available(context.Background()); err != nil {
		t.Fatalf("Available() error = %v", err)
	}
	logs, err := host.FetchFailedCheckLogs(context.Background(), testPR(), "feature/forgejo", strings.Repeat("b", 40), []string{"CI / test (pull_request)"})
	if err != nil || !strings.Contains(logs, "failed") || len(recorder.calls) != 3 {
		t.Fatalf("FetchFailedCheckLogs() = (%q, %v) with %d calls, want live-head log lookup", logs, err, len(recorder.calls))
	}
}

func TestFetchFailedCheckLogsRejectsInvalidFreshChecksBeforeRunLookup(t *testing.T) {
	target := testBaseURL + "/" + testRepo + "/actions/runs/91/jobs/0"
	statuses := fmt.Sprintf(`[{"context":"CI / test (pull_request)","state":"failure","target_url":%q}]`, target)
	checks := strings.Replace(checksJSON("failure", "not_required", false, statuses, `[]`), testHeadSHA, "", 1)
	recorder := &fakeRecorder{responses: []fakeResponse{
		{stdout: fixture(t, "status-forgejo-16.json")},
		{stdout: checks},
	}}
	host := newTestHost(recorder)
	if err := host.Available(context.Background()); err != nil {
		t.Fatalf("Available() error = %v", err)
	}
	logs, err := host.FetchFailedCheckLogs(context.Background(), testPR(), "feature/forgejo", testHeadSHA, []string{"CI / test (pull_request)"})
	if logs != "" || err == nil || !strings.Contains(err.Error(), "checks without a head SHA") || len(recorder.calls) != 2 {
		t.Fatalf("FetchFailedCheckLogs() = (%q, %v) with %d calls, want fresh-check validation error", logs, err, len(recorder.calls))
	}
}

func TestFetchFailedCheckLogsBoundsOutputAndHonorsCancellation(t *testing.T) {
	target := testBaseURL + "/" + testRepo + "/actions/runs/91/jobs/0"
	statuses := fmt.Sprintf(`[{"context":"CI / test (pull_request)","state":"failure","target_url":%q}]`, target)

	t.Run("stdout limit", func(t *testing.T) {
		recorder := &fakeRecorder{responses: []fakeResponse{
			{stdout: fixture(t, "status-forgejo-16.json")},
			{stdout: checksJSON("failure", "not_required", false, statuses, `[]`)},
			{stdoutBytes: maxForgejoLogOutputBytes + 128*1024},
		}}
		host := newTestHost(recorder)
		if err := host.Available(context.Background()); err != nil {
			t.Fatalf("Available() error = %v", err)
		}
		logs, err := host.FetchFailedCheckLogs(context.Background(), testPR(), "feature/forgejo", testHeadSHA, []string{"CI / test (pull_request)"})
		if logs != "" || err == nil || !strings.Contains(err.Error(), "exceeded 1048576 bytes") {
			t.Fatalf("FetchFailedCheckLogs() = (%q, %v), want bounded-output error", logs, err)
		}
	})

	t.Run("aggregate log limit", func(t *testing.T) {
		largeLog := strings.Repeat("x", maxForgejoLogOutputBytes/2)
		statuses := fmt.Sprintf(`[
			{"context":"CI / first (pull_request)","state":"failure","target_url":%q},
			{"context":"CI / second (pull_request)","state":"failure","target_url":%q}
		]`, testBaseURL+"/"+testRepo+"/actions/runs/91/jobs/0", testBaseURL+"/"+testRepo+"/actions/runs/92/jobs/0")
		recorder := &fakeRecorder{responses: []fakeResponse{
			{stdout: fixture(t, "status-forgejo-16.json")},
			{stdout: checksJSON("failure", "not_required", false, statuses, `[]`)},
			{stdout: failedLogRunViewJSON(91, testHeadSHA, []string{fmt.Sprintf(`{"id":501,"run_id":91,"name":"first","status":"failure","log":%q}`, largeLog)})},
			{stdout: failedLogRunViewJSON(92, testHeadSHA, []string{fmt.Sprintf(`{"id":502,"run_id":92,"name":"second","status":"failure","log":%q}`, largeLog)})},
		}}
		host := newTestHost(recorder)
		if err := host.Available(context.Background()); err != nil {
			t.Fatalf("Available() error = %v", err)
		}
		logs, err := host.FetchFailedCheckLogs(context.Background(), testPR(), "feature/forgejo", testHeadSHA, []string{
			"CI / first (pull_request)", "CI / second (pull_request)",
		})
		if logs != "" || err == nil || !strings.Contains(err.Error(), "failed check logs exceeded 1048576 bytes") {
			t.Fatalf("FetchFailedCheckLogs() = (%d bytes, %v), want aggregate-limit error", len(logs), err)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		recorder := &fakeRecorder{responses: []fakeResponse{
			{stdout: fixture(t, "status-forgejo-16.json")},
			{stdout: checksJSON("failure", "not_required", false, statuses, `[]`)},
			{sleep: 2 * time.Second},
		}}
		host := newTestHost(recorder)
		if err := host.Available(context.Background()); err != nil {
			t.Fatalf("Available() error = %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := host.FetchFailedCheckLogs(ctx, testPR(), "feature/forgejo", testHeadSHA, []string{"CI / test (pull_request)"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("FetchFailedCheckLogs() error = %v, want deadline exceeded", err)
		}
	})
}

func TestFetchFailedCheckLogsRedactsCommandErrors(t *testing.T) {
	target := testBaseURL + "/" + testRepo + "/actions/runs/91/jobs/0"
	statuses := fmt.Sprintf(`[{"context":"CI / test (pull_request)","state":"failure","target_url":%q}]`, target)
	recorder := &fakeRecorder{responses: []fakeResponse{
		{stdout: fixture(t, "status-forgejo-16.json")},
		{stdout: checksJSON("failure", "not_required", false, statuses, `[]`)},
		{stdout: `{"error":"log failed with secret-token","code":"LOG_ERROR","details":{"url":"https://user:pass@forge.example/log?token=secret-token"}}`, code: 1},
	}}
	host := newTestHost(recorder)
	if err := host.Available(context.Background()); err != nil {
		t.Fatalf("Available() error = %v", err)
	}
	_, err := host.FetchFailedCheckLogs(context.Background(), testPR(), "feature/forgejo", testHeadSHA, []string{"CI / test (pull_request)"})
	if err == nil || !strings.Contains(err.Error(), "LOG_ERROR") || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "user:pass") {
		t.Fatalf("FetchFailedCheckLogs() error = %v, want code with secrets redacted", err)
	}
}

func TestFetchFailedCheckLogsRejectsNonCanonicalTargetsWithoutGuessing(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{name: "external host", target: "https://ci.example/actions/runs/91/jobs/0"},
		{name: "wrong repository", target: testBaseURL + "/other/widgets/actions/runs/91/jobs/0"},
		{name: "wrong base prefix", target: "https://forge.example:3443/other/octo/widgets/actions/runs/91/jobs/0"},
		{name: "missing target", target: ""},
		{name: "zero run", target: testBaseURL + "/" + testRepo + "/actions/runs/0/jobs/0"},
		{name: "leading-zero run", target: testBaseURL + "/" + testRepo + "/actions/runs/091/jobs/0"},
		{name: "signed run", target: testBaseURL + "/" + testRepo + "/actions/runs/+91/jobs/0"},
		{name: "leading-zero job", target: testBaseURL + "/" + testRepo + "/actions/runs/91/jobs/00"},
		{name: "signed job", target: testBaseURL + "/" + testRepo + "/actions/runs/91/jobs/+0"},
		{name: "malformed job index", target: testBaseURL + "/" + testRepo + "/actions/runs/91/jobs/latest"},
		{name: "empty query ambiguity", target: testBaseURL + "/" + testRepo + "/actions/runs/91/jobs/0?"},
		{name: "query ambiguity", target: testBaseURL + "/" + testRepo + "/actions/runs/91/jobs/0?attempt=2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var target any
			if tt.target != "" {
				target = tt.target
			}
			statuses, err := json.Marshal([]map[string]any{{
				"context": "CI / test (pull_request)", "state": "failure", "target_url": target,
			}})
			if err != nil {
				t.Fatal(err)
			}
			recorder := &fakeRecorder{responses: []fakeResponse{
				{stdout: fixture(t, "status-forgejo-16.json")},
				{stdout: checksJSON("failure", "not_required", false, string(statuses), `[]`)},
			}}
			host := newTestHost(recorder)
			if err := host.Available(context.Background()); err != nil {
				t.Fatalf("Available() error = %v", err)
			}
			logs, err := host.FetchFailedCheckLogs(context.Background(), testPR(), "feature/forgejo", testHeadSHA, []string{"CI / test (pull_request)"})
			if err != nil || logs != "" {
				t.Fatalf("FetchFailedCheckLogs() = (%q, %v), want unavailable without error", logs, err)
			}
			if len(recorder.calls) != 2 {
				t.Fatalf("calls = %d, want status + checks only; target must not trigger run guessing", len(recorder.calls))
			}
		})
	}
}

func failedLogRunViewJSON(runID int, headSHA string, jobs []string) string {
	return fmt.Sprintf(`{
		"run":{"id":%d,"url":%q,"api_url":%q,"title":"CI","event":"pull_request","branch":"feature/forgejo","head_sha":%q,"run_number":7,"status":"failure","started_at":null,"completed_at":"2026-08-06T00:01:00Z"},
		"jobs":[%s]
	}`, runID, testBaseURL+"/"+testRepo+"/actions/runs/"+strconv.Itoa(runID), testBaseURL+"/api/v1/repos/"+testRepo+"/actions/runs/"+strconv.Itoa(runID), headSHA, strings.Join(jobs, ","))
}
