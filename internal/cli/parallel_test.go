package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// allowSerial names every test that stays sequential on purpose: the test
// and the seam that keeps it there (design yardr-14tr, section 3.3). A
// test that is neither parallel nor listed fails the gate, so a future
// test cannot join the serial set silently; a listed test that turned
// parallel, that no longer exists, or that reaches no process or package
// state fails it too.
var allowSerial = map[string]string{
	"TestAddrSettingsAreCheckedAsTheFlag":                   "stubWeb,t.Setenv",
	"TestAskTelemetrySkipsWhenSet":                          "stubAsk",
	"TestBDPAndOTelStartStop":                               "running,stub:answers,stub:spawn,t.Setenv",
	"TestBDPStartAndInstallStayOnLoopback":                  "stub:answers,stub:spawn,stubServices",
	"TestBareYardr":                                         "holdDaemon,setInteractive,stubAsk,stubFocus,stubLookPath,stubPane,stubStart",
	"TestBareYardrFinishesAYardWithoutDefaultKind":          "resumeYard,startsYardmaster,stubPane",
	"TestBareYardrLeavesOtherYardsAlone":                    "holdDaemon,resumeYard,setInteractive,startsYardmaster",
	"TestBareYardrStartsAYardThatDidNotStart":               "resumeYard,startsYardmaster,stubPane",
	"TestBeadAskDepotPush":                                  "depotPushCLIYard",
	"TestBeadListWithClosedIsCut":                           "stub:listLimit",
	"TestChatCommand":                                       "chatYard",
	"TestChatHookIsNotQuietBySilence":                       "stub:hookNow",
	"TestChatNeedsATerminal":                                "assign:interactive,chatYard",
	"TestChatWithoutCrew":                                   "chatYard",
	"TestCrewAddOnAnExistingCrew":                           "setInteractive,stubAsk",
	"TestCrewAnswersWithAProvidersCredential":               "newAppYard,t.Setenv",
	"TestCrewWithoutGitHubCredentialIsRefused":              "newAppYard,t.Setenv",
	"TestDaemonClosesHerdrSessionsWhenItsHomeIsGone":        "daemonUp",
	"TestDaemonClosesSessionsStartedSinceItsLastPass":       "daemonUp",
	"TestDaemonInstallFileOnly":                             "stubServices",
	"TestDaemonInstallNamesHerdrLogin":                      "stubLookPath,stubServices",
	"TestDaemonInstallStopsTheRunningDaemon":                "holdDaemon,stub:signalStop,stubServices",
	"TestDaemonInstallSystemd":                              "stubLookPath,stubServices",
	"TestDaemonInstallUninstall":                            "stub:userHomeDir,stubLookPath,stubServices",
	"TestDaemonKeepsHerdrSessionsOnStop":                    "daemonUp",
	"TestDaemonServiceReachesTheHerdrSessionSet":            "stubLookPath,stubServices,t.Setenv",
	"TestDaemonSkipsHerdrSessionsItCannotClose":             "daemonUp",
	"TestDaemonStopAndStatus":                               "holdDaemon,stubServices",
	"TestDaemonStopLeavesAReusedPIDAlone":                   "stub:signalStop",
	"TestDaemonStopRefusesAPidItCannotVerify":               "stub:signalStop",
	"TestDaemonStopsWhenItsTestOwnerIsGone":                 "t.Setenv",
	"TestDaemonWithoutYard":                                 "t.Setenv",
	"TestDefaultClaudeAccount":                              "t.Setenv",
	"TestDefaultOperator":                                   "assign:loginName,noLogin,t.Setenv",
	"TestDefaultOperatorBlockedLookup":                      "assign:osUserLookup,t.Setenv",
	"TestDefaultPackSetting":                                "t.Chdir",
	"TestDepotInitPackWritesNothingUnasked":                 "setInteractive,stubAsk",
	"TestDocsListNamesEveryPage":                            "noHome",
	"TestDocsShowPrintsOnePage":                             "noHome",
	"TestDocsShowPrintsOneSection":                          "noHome",
	"TestDoctorBlockerCycle":                                "answeredTools",
	"TestDoctorCodexTrust":                                  "answeredTools,t.Setenv",
	"TestDoctorCrewNotLoggedIn":                             "answeredTools",
	"TestDoctorDaemonLost":                                  "answeredTools",
	"TestDoctorEmptyHome":                                   "fakeTools",
	"TestDoctorFailingCrew":                                 "answeredTools",
	"TestDoctorFetchOnMac":                                  "fakeTools,stub:goarch,stub:goos,stub:loginShell,stub:userHomeDir",
	"TestDoctorFirstOnPath":                                 "stub:loginShell,t.Setenv",
	"TestDoctorFreshYard":                                   "answeredTools",
	"TestDoctorHarnessLabels":                               "fakeTools,stub:loginShell",
	"TestDoctorHerdrLoginService":                           "answeredTools,stubServices",
	"TestDoctorHerdrServer":                                 "fakeTools,stub:herdrServer",
	"TestDoctorKimiHooks":                                   "answeredTools,stub:userHomeDir",
	"TestDoctorLeavesAStoppedServerStopped":                 "fakeTools,holdDaemon,stub:connect",
	"TestDoctorLoginServiceDepots":                          "answeredTools,stub:goos,stub:loginService,stub:userHomeDir",
	"TestDoctorLoginShellFinds":                             "answeredTools,stub:loginShell,stub:loginShellTimeout",
	"TestDoctorLoginShellFindsHarnessForNewYard":            "answeredTools,stub:loginShell",
	"TestDoctorMissingDependency":                           "answeredTools",
	"TestDoctorNamesForeignFolderLogin":                     "t.Setenv",
	"TestDoctorNamesTheHerdrSession":                        "fakeTools,otherShell,stub:herdrServer",
	"TestDoctorNoFetchOfWhatIsFound":                        "fakeTools,stub:goarch,stub:goos,stub:herdrServer",
	"TestDoctorNoFetchOnLinux":                              "fakeTools,stub:goarch,stub:goos,stub:loginShell,stub:userHomeDir",
	"TestDoctorNotesQuietHook":                              "stub:answers,stub:hookNow",
	"TestDoctorOffers":                                      "fakeTools,stub:loginShell",
	"TestDoctorOffersAiquokka":                              "stubLookPath",
	"TestDoctorSaysWhatTheReceiverListensOn":                "answeredTools",
	"TestDoctorSetUpYard":                                   "answeredTools",
	"TestDoctorTry":                                         "fakeTools,stub:tryHarnessCall,stub:tryTimeout,t.Setenv",
	"TestDoctorUnreachableServer":                           "answeredTools",
	"TestDoctorVersionCheck":                                "fakeTools,stub:goos,stub:versionTimeout",
	"TestDoctorWebView":                                     "holdDaemon,statusYard,stubServices",
	"TestDoctorYardmasterGivenUpAfterSessionsGoneAtOnce":    "answeredTools",
	"TestDownAsksInATerminal":                               "downYard,stub:ask,stub:interactive",
	"TestDownDeclineStopsInspectionServer":                  "downYard,stub:ask,stub:interactive,stub:serverRunning",
	"TestDownDoesNotAskAboutIdleAgents":                     "downYard",
	"TestDownFailsWhenOptionalServiceCannotStop":            "downYard,stub:signalStop",
	"TestDownLoginRemovesNothingBeforeItStops":              "downYard,stubServices",
	"TestDownSaysWhatItCouldNotStop":                        "downYard",
	"TestDownStopsAStartThatWasUnderway":                    "downYard",
	"TestDownStopsCrewConversationSessions":                 "downYard",
	"TestDownStopsTheYardsAgents":                           "downYard",
	"TestDownWithHerdrsServerDown":                          "downYard,stub:herdrServer",
	"TestEnvironmentNamesTheSessionOutsideAWorktree":        "t.Chdir",
	"TestFakeToolsVersionTimeout":                           "fakeTools",
	"TestHandOverAsSomeoneWhoIsNoUser":                      "setupYard,stubChat,stubFocus,stubPane",
	"TestHandOverReportsAViewThatFails":                     "setupYard,stub:runChat,stubPane",
	"TestHarnessListAndShow":                                "noHome",
	"TestHarnessLoginEndsInTheBrowser":                      "leadStandIn",
	"TestHarnessLoginFails":                                 "leadStandIn",
	"TestHarnessLoginGroupLandsInGroupFolder":               "leadStandInAs,t.Setenv",
	"TestHarnessLoginGroupRefusals":                         "fakeTools,noHome",
	"TestHarnessLoginInputGivenThroughSecretAsk":            "leadStandInGated",
	"TestHarnessLoginInputTimesOut":                         "leadStandInAs,stub:loginInputTimeout",
	"TestHarnessLoginLedByJSON":                             "leadStandIn",
	"TestHarnessLoginRecipeShown":                           "fakeTools,stub:loginShell",
	"TestHarnessLoginRedactsEchoedInput":                    "leadStandIn",
	"TestHarnessLoginRefusesAnswers":                        "leadStandIn",
	"TestHarnessLoginStdinAnswerLeavesTheWait":              "leadStandInAs",
	"TestHarnessLoginStdinClosed":                           "leadStandIn",
	"TestHarnessLoginStopsWithItsLeader":                    "leadStandIn",
	"TestHarnessLoginTimesOut":                              "leadStandIn,stub:loginTimeout",
	"TestHarnessLoginWithoutARecipe":                        "fakeTools,noHome",
	"TestHarnessOptions":                                    "fakeTools,stubLookPath",
	"TestHerdrLoginRejectsUnsafeSessionName":                "stubServices,t.Setenv",
	"TestHerdrServiceOfANamedSession":                       "stubServices",
	"TestHomeFlagInTheRefusal":                              "noHome,t.Setenv",
	"TestHomeFlagInstallsTheServiceForThatHome":             "noHome,stubLookPath,stubServices,t.Setenv",
	"TestHomeFlagOnboards":                                  "noHome,setInteractive,stubAsk,stubFocus,stubLookPath,stubPane,stubStart",
	"TestHomeFlagOrder":                                     "answeredTools,noHome,t.Setenv",
	"TestHomeFlagReachesChildren":                           "answeredTools,noHome,t.Chdir,t.Setenv",
	"TestHomeFlagTooLong":                                   "answeredTools,noHome",
	"TestHookAddMap":                                        "stateYard",
	"TestHookAddNamesTheEdgeWhereTheLinkServesAlone":        "answeredTools",
	"TestHookListShowsQuiet":                                "stub:hookNow",
	"TestInitFinishesAfterAFailedRun":                       "t.Setenv",
	"TestInitNamesTelemetrySwitch":                          "setInteractive,stubAsk",
	"TestInitOperatorDefaultsToUser":                        "t.Setenv",
	"TestInitRecordsTheYardrItRuns":                         "stub:executable",
	"TestInitStart":                                         "stubLookPath,stubPane,stubStart",
	"TestInitStartAsksAgainWhileTheYardmasterIsBlocked":     "blockedYardmaster",
	"TestInitStartAsksOnlyWhereTheChatOpens":                "blockedYardmaster,setInteractive",
	"TestInitStartAsksTheHarnessInATerminalOnly":            "newAsker,setInteractive",
	"TestInitStartEndsInTheChatInATerminalOnly":             "actAsOperator,setInteractive,stubChat,stubLookPath,stubPane,stubStart",
	"TestInitStartInHerdrShowsThePane":                      "blockedYardmaster,t.Setenv",
	"TestInitStartMakesOneCallAfterAFailure":                "newAsker,setInteractive,stub:pollEvery,stubPane,stubStart",
	"TestInitStartNotNow":                                   "blockedYardmaster,setInteractive",
	"TestInitStartOffersNoPaneOfAYardmasterThatDidNotStart": "blockedYardmaster,stubStart",
	"TestInitStartOffersTheYardmastersPane":                 "blockedYardmaster",
	"TestInitStartTriesOnceMoreAfterAFailure":               "newAsker,stub:pollEvery,stubPane,stubStart",
	"TestInitStartWhenThePaneDoesNotOpen":                   "blockedYardmaster,stub:attachAgent",
	"TestInitStartWithRunningDaemon":                        "stubLookPath,stubStart",
	"TestInitSummaryNamesTheHome":                           "t.Setenv",
	"TestInitWithoutLoginName":                              "noLogin",
	"TestInstallDownload":                                   "stub:versionTimeout",
	"TestInstallMakesTheLogPrivate":                         "stubLookPath,stubServices",
	"TestKillStrayLeavesAReusedPidAlone":                    "assign:strayDescribe,assign:strayKill",
	"TestLeadStandInGatedFreesAHeldGateWhenItsTestEnds":     "leadStandInGated",
	"TestLimitsGroupReadsTheGroupsLogin":                    "t.Setenv",
	"TestLoginNameNobodyHas":                                "chatYard",
	"TestLoginNotSupported":                                 "stubServices,upYard",
	"TestLoginShellPATH":                                    "stub:loginShell,stub:loginShellTimeout",
	"TestMailAUser":                                         "t.Chdir",
	"TestMCPServeOverStdio":                                 "assign:mcpServe",
	"TestNoCommandReachesTheMachinesYard":                   "t.Setenv",
	"TestNoPairFormWithoutATerminal":                        "setInteractive,stubAsk",
	"TestNoQuestionWithoutACallOrAStart":                    "newAsker,setInteractive,stubLookPath",
	"TestOTelInstall":                                       "stubServices",
	"TestOnboardingAfterAFailedRun":                         "resumeYard,startsYardmaster,stubPane",
	"TestOnboardingAsYardrActor":                            "onboardTo,stubChat,stubServices,stubWeb",
	"TestOnboardingAsksForTheServices":                      "onboardTo,stubServices,stubWeb",
	"TestOnboardingAsksTheHarnessFirst":                     "newAsker",
	"TestOnboardingAsksTheStyle":                            "onboardTo,stubServices,stubWeb",
	"TestOnboardingCancelled":                               "setInteractive,stubAsk,stubLookPath",
	"TestOnboardingForm":                                    "setInteractive,stubAsk,stubFocus,stubLookPath,stubPane,stubStart",
	"TestOnboardingGoesOnWithoutAnAnswer":                   "newAsker",
	"TestOnboardingInstallsTheDaemonAndStartsTheWebView":    "onboardTo,stubServices,stubWeb",
	"TestOnboardingInstallsTheWebView":                      "setInteractive,stubAsk,stubFocus,stubLookPath,stubPane,stubServices,stubStart,stubWeb",
	"TestOnboardingIsTakenUpWhereTheYardmasterDidNotComeUp": "resumeYard,startsYardmaster,stub:spawnDaemon,stubPane",
	"TestOnboardingKeepsGoing":                              "setInteractive,stubAsk,stubFocus,stubLookPath,stubPane,stubStart",
	"TestOnboardingNoStart":                                 "setInteractive,stubAsk,stubLookPath,stubStart",
	"TestOnboardingOffersTheYardmastersPane":                "blockedYardmaster,stub:yardmasterWait",
	"TestOnboardingPreflight":                               "setInteractive,stubAsk,stubFocus,stubLookPath,stubPane,stubStart",
	"TestOnboardingSkipsTheCall":                            "newAsker",
	"TestOnboardingStartsTheYard":                           "actAsOperator,setInteractive,stubAsk,stubChat,stubFocus,stubLookPath,stubPane,stubStart,t.Setenv",
	"TestOnboardingStopsBeforeTheYardmaster":                "newAsker",
	"TestOnboardingTelemetryYes":                            "setInteractive,stubAsk,stubFocus,stubLookPath,stubPane,stubStart",
	"TestOnboardingTriesTheHarnessAgain":                    "newAsker,stubAsk",
	"TestOnboardingWithASilentYardmasterOpensNoChat":        "actAsOperator,setInteractive,stub:yardmasterWait,stubAsk,stubChat,stubLookPath,stubPane,stubStart",
	"TestOnboardingWithoutAServiceManager":                  "onboardTo,stubServices",
	"TestOnboardingWithoutAYardmasterSaysWhatDoctorSays":    "actAsOperator,resumeYard,stub:spawnDaemon,stubChat",
	"TestOpenThreadsIsTheOperatorsToSet":                    "openYard",
	"TestOwnGroupCancelDoesNotSignalWhenIdentityIsUnknown":  "assign:ownGroupDescribe,assign:ownGroupKill",
	"TestOwnGroupCancelLeavesAReusedPidAlone":               "assign:ownGroupDescribe,assign:ownGroupKill",
	"TestOwnGroupCancelStillCleansAGroupWithoutItsLeader":   "assign:ownGroupDescribe,assign:ownGroupKill",
	"TestOwnGroupCancelWhileRunningKillsTheGroup":           "assign:ownGroupKill",
	"TestPackApplyAsksInATerminal":                          "setInteractive,stubAsk",
	"TestPackApplyAsksPermissionsInATerminal":               "setInteractive,stubAsk",
	"TestPackApplyAsksYardPresetInATerminal":                "setInteractive,stubAsk",
	"TestPackApplyAsksTheOperatorAboutCalls":                "setInteractive,stubAsk",
	"TestPackApplyAsksTheOperatorAboutOpeningThreads":       "answerForms,setInteractive",
	"TestPackApplyAsksTheOperatorWhomToExposeTo":            "answerExpose,setInteractive",
	"TestPackApplyAsksWhichGroupTakes":                      "setInteractive,stubAsk",
	"TestPackApplyExposesNobodyUnasked":                     "answerExpose,setInteractive",
	"TestPackApplyOnlyTheOperatorGrants":                    "answerForms,setInteractive",
	"TestPackApplyOnlyTheOperatorGrantsCalls":               "setInteractive,stubAsk",
	"TestPackApplyOpenThreadsFlag":                          "answerForms,setInteractive",
	"TestPackApplyRefusesAHandEditedCopy":                   "setInteractive",
	"TestPackByNameOfAnImport":                              "t.Chdir",
	"TestPackNameIsNeverADirectory":                         "t.Chdir",
	"TestPagesSettingsAreCheckedAsTheFlags":                 "stubWeb",
	"TestPairForm":                                          "formYards",
	"TestPairFormAsksAgainAfterAWrongCode":                  "pairYards,setInteractive,stubAsk",
	"TestPairFormAsksWhoMayBeWrittenTo":                     "formYards",
	"TestPairFormAsksWhomToTrust":                           "stubAsk",
	"TestPairFormExposesWithoutMail":                        "formYards",
	"TestPairFormFileGoesWithMail":                          "stubAsk",
	"TestPairFormKeepsPing":                                 "stubAsk",
	"TestPairFormLeftAfterTheCheck":                         "formYards",
	"TestPairFormNoAtTheCheck":                              "formYards",
	"TestPairFormSetsTheAddress":                            "setInteractive,stub:answers,stub:listenWait,stub:restartServer,stubAsk",
	"TestPairFormWithFile":                                  "formYards",
	"TestPairFormWithoutMail":                               "formYards",
	"TestPairFormYardThatCannotBeCalled":                    "setInteractive,stub:restartServer,stubAsk",
	"TestPeerPair":                                          "pairYards",
	"TestPeerPairAPeerAlready":                              "assign:askCheck,pairYards,setInteractive",
	"TestPeerPairCallFails":                                 "pairYards,setInteractive",
	"TestPeerPairCallNotAnswered":                           "pairYards,setInteractive,stub:pairHTTP",
	"TestPeerPairDeclined":                                  "assign:askCheck,pairYards,setInteractive",
	"TestPeerPairExposesFromFlag":                           "pairYards",
	"TestPeerPairJSON":                                      "assign:pairCommand,pairYards",
	"TestPeerPairNeedsAListener":                            "pairYards",
	"TestPeerPairWarnsOfALoopbackListener":                  "assign:pairCommand,pairYards",
	"TestPeerPairWithoutATerminal":                          "pairYards,setInteractive",
	"TestPreflightOffersToInstallHerdr":                     "offerEnv",
	"TestPreflightSaysWhereToGet":                           "answeredTools,stubAsk",
	"TestPurgeAsksForTheYardsNameInATerminal":               "purgeYard,stub:ask,stub:interactive",
	"TestPurgeDeletesTheHomeAndKeepsTheWork":                "purgeYard",
	"TestPurgeIsTheOperators":                               "purgeYard",
	"TestPurgeKeepsADepotUnderAnotherSpellingOfTheHome":     "purgeYard",
	"TestPurgeNeedsTheYardsName":                            "purgeYard",
	"TestPurgeRefusesAHomeThatIsMoreThanAYard":              "downYard,stub:userHomeDir,stubServices",
	"TestPurgeRemovesAHomeWithNothingKept":                  "downYard,stubServices",
	"TestRecordStopLeavesAReusedPIDAlone":                   "stub:signalStop",
	"TestRecordStopRefusesAPidItCannotVerify":               "stub:signalStop",
	"TestReply":                                               "t.Setenv",
	"TestReplyDeclaredSource":                                 "t.Setenv",
	"TestReplyDeclaredSourceMinted":                           "t.Setenv",
	"TestReplyOnlyByTheCrewOrOperator":                        "t.Setenv",
	"TestReplyPlacement":                                      "t.Setenv",
	"TestReplyRefusals":                                       "t.Setenv",
	"TestReplyRetryDoesNotPostTwice":                          "t.Setenv",
	"TestReviewRefusesStaleDecision":                          "assign:ask",
	"TestSecondDaemonRefused":                                 "stub:daemonLockWait,t.Setenv",
	"TestServeTakesTheAddrSetting":                            "statusYard",
	"TestServeTakesThePagesSetting":                           "statusYard",
	"TestServiceNameCarriesTheHome":                           "stubServices",
	"TestServiceNameWithoutHome":                              "stubServices",
	"TestServiceNotSupported":                                 "stubServices",
	"TestSetStateIsTheOperatorsToSet":                         "stateYard",
	"TestSettingsGetPrintsTheValueInEffect":                   "t.Setenv",
	"TestSettingsSetHerdrSessionWhileCrewRuns":                "statusYard",
	"TestSetupAfterAStartByHand":                              "setupYard",
	"TestSetupBaseThatIsNoLocalBranch":                        "setupYard",
	"TestSetupCancelled":                                      "setupYard",
	"TestSetupDirDepot":                                       "setupYard",
	"TestSetupForms":                                          "setupYard",
	"TestSetupGoesOnWhereTheYardStands":                       "setupYard",
	"TestSetupGuideShowsNoCommands":                           "setupYard",
	"TestSetupOffersTheLink":                                  "actAsOperator,onboardTo,stubAsk,stubServices,stubWeb",
	"TestSetupOffersToBeToldOfLandedBeads":                    "setupYard",
	"TestSetupTalkToTheYardmaster":                            "setupYard,stubChat,stubFocus,stubPane,t.Setenv",
	"TestSetupTalkToTheYardmasterOffersItsPane":               "setupYard,stub:attachAgent,stub:paneTail,stub:pollEvery,stub:yardmasterWait,stubChat,t.Setenv",
	"TestSetupWithoutForms":                                   "setInteractive,setupYard",
	"TestSourceAddAKnownName":                                 "t.Chdir",
	"TestSpeakingAsAGitHubApp":                                "newAppYard",
	"TestStartAndInstallTakeTheAddrSetting":                   "stub:answers,stub:spawn,stubServices,t.Setenv",
	"TestStartAndInstallTakeThePagesFlags":                    "stub:answers,stub:spawn,stubServices,t.Setenv",
	"TestStatusAndDoctorSayActsAsFromSetting":                 "statusYard",
	"TestStatusAndDoctorSayJWTFrom":                           "statusYard",
	"TestStatusBackups":                                       "statusYard",
	"TestStatusBackupsWhenServerIsStopped":                    "stub:connect,stub:serverRunning",
	"TestStatusDaemonLost":                                    "answeredTools,stub:serverRunning",
	"TestStatusLeavesAStoppedServerStopped":                   "holdDaemon,stub:connect,stub:machineAddr",
	"TestStatusNamesTheHerdrSession":                          "otherShell,statusYard",
	"TestStatusNoYard":                                        "setInteractive,stubAsk",
	"TestStatusRunningYard":                                   "holdDaemon,statusYard",
	"TestStatusSameBuild":                                     "statusYard",
	"TestStatusServerOnly":                                    "statusYard",
	"TestStatusStaleWebView":                                  "statusYard",
	"TestStatusUnreachableServer":                             "answeredTools,stub:serverRunning",
	"TestStatusWebViewAs":                                     "statusYard",
	"TestStatusWebViewBuild":                                  "statusYard,stub:viewRevision",
	"TestStatusWorkLine":                                      "statusYard",
	"TestStopSkipsAPidGoneBeforeItsSignal":                    "stub:signalStop",
	"TestStopWaits":                                           "stub:signalStop",
	"TestStoreSendsTheRunsSession":                            "assign:connect",
	"TestTheNextRunWaitsForAYardmasterThatWasNotUp":           "holdDaemon,resumeYard,startsYardmaster,stubPane",
	"TestThreadCommandsDeclaredSourceBase":                    "t.Setenv",
	"TestThreadOpen":                                          "openYard",
	"TestThreadOpenAssign":                                    "t.Setenv",
	"TestThreadOpenDeclaredSource":                            "t.Setenv",
	"TestThreadOpenRefusals":                                  "openYard,t.Setenv",
	"TestThreadOpenRetryDoesNotOpenTwice":                     "openYard",
	"TestThreadShow":                                          "t.Setenv",
	"TestThreadShowDeclaredSource":                            "t.Setenv",
	"TestThreadStateAcme":                                     "stateYard",
	"TestThreadStateDeclaredSource":                           "t.Setenv",
	"TestThreadStateGitHub":                                   "stateYard",
	"TestThreadStateGitHubLabel":                              "stateYard",
	"TestThreadStateRefusals":                                 "stateYard,t.Setenv",
	"TestThreeDownsHoldNoBead":                                "downYard",
	"TestTwoYardsShareHerdrsLoginService":                     "stubLogin,stubLookPath,upYard",
	"TestUninstallLeavesAnotherYardsServices":                 "stubLogin,t.Setenv,upYard",
	"TestUninstallListsAServiceItCouldNotRemove":              "stubLogin,upYard",
	"TestUninstallOnNoYard":                                   "stub:connect,stubLookPath,stubServices",
	"TestUninstallRemovesNothingBeforeItStops":                "downYard,stubServices",
	"TestUninstallStopsTheYardAndRemovesItsServices":          "stub:serverRunning,stubLogin,stubLookPath,upYard",
	"TestUninstallWhereThereIsNoLoginService":                 "downYard,stubServices",
	"TestUpAndDownOnNoYard":                                   "stub:connect",
	"TestUpDownStartsTheServiceWebView":                       "running,stub:answers,stub:serviceCtl,stub:spawn,stub:stopServer,stub:yardRunners,stubServices,upYard",
	"TestUpFindsHerdrWithTheLoginShell":                       "stub:loginShell,stub:loginShellFinds,stub:loginShellPATH,stub:spawn,stub:spawnHerdr,stubLookPath,t.Setenv,upYard",
	"TestUpLoginInstallsAndStarts":                            "stubLogin,upYard",
	"TestUpLoginInstallsTheWebViewTheYardNames":               "stubLogin,upYard",
	"TestUpLoginLeavesARunningServer":                         "stubLogin,stubLookPath,upYard",
	"TestUpLoginLeavesAServiceThatIsNotYardrs":                "stubLogin,upYard",
	"TestUpLoginWritesTheLoginShellsPATH":                     "stub:loginShellPATH,stubLogin,upYard",
	"TestUpReadsHistoryOnlyWhenThePromptLeftTheScreen":        "stub:paneHistory,stub:paneTail",
	"TestUpRefusesTheBDPServerOffLoopback":                    "stub:answers,stub:spawn,upYard",
	"TestUpStartsTheDaemonThroughItsLoginService":             "stubLogin,upYard",
	"TestUpStartsTheWebViewTheYardNames":                      "stub:answers,stub:spawn,upYard",
	"TestUpStartsWhatDoesNotRun":                              "stub:serverRunning,upYard",
	"TestUpWithoutAServiceUsesTheLoginShellsPATH":             "stub:loginShellPATH,stub:serviceDir,stub:spawn,t.Setenv,upYard",
	"TestUpWithoutHerdr":                                      "stubLookPath,t.Setenv,upYard",
	"TestUpgradeCheckAllOK":                                   "holdDaemon,stubServer,upgradeYard",
	"TestUpgradeCheckDaemonBuildIsLatestStart":                "holdDaemon,stubServer,upgradeYard",
	"TestUpgradeCheckDaemonFlap":                              "stub:upgradeDaemonRunning,stubServer,upgradeYard",
	"TestUpgradeCheckDaemonNoBuild":                           "holdDaemon,stubServer,upgradeYard",
	"TestUpgradeCheckDaemonOtherBuild":                        "holdDaemon,stubServer,upgradeYard",
	"TestUpgradeCheckDaemonWedged":                            "holdDaemon,stubServer,upgradeYard",
	"TestUpgradeCheckDeadView":                                "stubServer,upgradeYard",
	"TestUpgradeCheckJSONShape":                               "stubServer,upgradeYard",
	"TestUpgradeCheckMixedViews":                              "stubServer,upgradeYard",
	"TestUpgradeCheckOldView":                                 "stubServer,upgradeYard",
	"TestUpgradeCheckServerDown":                              "stubServer,upgradeYard",
	"TestUpgradeCheckServerOtherBuild":                        "stubServer,upgradeYard",
	"TestUpgradeCheckSinceFlag":                               "stubServer,upgradeYard",
	"TestUpgradeCheckViewAnswersOn500":                        "stubServer,upgradeYard",
	"TestUpgradeCheckViewOtherBuildByHand":                    "stubServer,upgradeYard",
	"TestUpgradeCheckViewOtherBuildFails":                     "stub:webService,stubServer,upgradeYard",
	"TestUpgradeCheckViewServiceCurrent":                      "stub:webService,stubServer,upgradeYard",
	"TestUpgradeCoveredYards":                                 "stub:goos,stub:serviceDir",
	"TestUpgradeHatch":                                        "stub:upgradeNotifySend",
	"TestUpgradeInstall":                                      "stub:upgradeExecutable,stub:upgradeGoBuild,t.Setenv",
	"TestUpgradeInstallTarget":                                "stub:upgradeExecutable,t.Setenv",
	"TestUpgradePollDeadlineCancelsCheck":                     "stub:upgradeRunInstalled",
	"TestUpgradePollFailure":                                  "stub:upgradeRunInstalled",
	"TestUpgradePollRunsInstalledBinary":                      "stub:upgradeRunInstalled",
	"TestUpgradeRestartViews":                                 "stub:serviceCtl,stubServices",
	"TestUpgradeRestartViewsRefused":                          "stub:serviceCtl,stubServices",
	"TestUpgradeFromFlag":                                     "stubNoRelease",
	"TestUpgradeFromFlagOperatorOnly":                         "stubNoRelease",
	"TestUpgradeReleaseAlreadyLatest":                         "newReleaseScene",
	"TestUpgradeReleaseBadSignature":                          "newReleaseScene",
	"TestUpgradeReleaseGateFailureRollsBack":                  "newReleaseScene",
	"TestUpgradeReleaseInstalls":                              "newReleaseScene",
	"TestUpgradeReleaseNoNetwork":                             "newReleaseScene",
	"TestUpgradeReleaseOlderIsNotInstalled":                   "newReleaseScene",
	"TestUpgradeReleaseOperatorOnly":                          "newReleaseScene",
	"TestUpgradeReleaseTamperedArchive":                       "newReleaseScene",
	"TestUpgradeReleaseUnsigned":                              "newReleaseScene",
	"TestUpgradeRestoreOn":                                    "t.Setenv",
	"TestUpgradeRunInstall":                                   "newUpgradeScene,t.Setenv",
	"TestUpgradeRunRefreshesScripts":                          "newUpgradeScene",
	"TestUpgradeRunRollbackPutsScriptsBack":                   "newUpgradeScene",
	"TestUpgradeRunScriptsCurrent":                            "newUpgradeScene",
	"TestUpgradeWithoutCheck":                                 "stubNoRelease",
	"TestUpgradeScriptsModeIgnoresUmask":                      "syscall.Umask",
	"TestUseLoginPATH":                                        "stub:loginShellPATH,t.Setenv",
	"TestUserAddWithALogin":                                   "fakeTools,stub:tryHarnessCall",
	"TestUserAddWithALoginNobodyFinishes":                     "fakeTools,stub:loginInputTimeout,stub:tryHarnessCall",
	"TestUserAddWithALoginThatAnswersNothing":                 "fakeTools,stub:tryHarnessCall",
	"TestUserListAndMailToThoseALinkedYardExposes":            "assign:interactive,assign:runChat",
	"TestVersionFlagPrintsWhatVersionDoes":                    "noHome",
	"TestWaitEndsOnAFailedStart":                              "assign:spawnDaemon,stubLookPath",
	"TestWaitEndsOnFirstOutput":                               "assign:paneTail,stubLookPath,stubStart",
	"TestWaitForTheYardmastersOutput":                         "assign:agentStatus,stubLookPath,stubPane,stubStart",
	"TestWaitSaysTheHarnessWaitsInItsPane":                    "assign:pollEvery,assign:screenSettle,stubLookPath,stubPane,stubStart",
	"TestWatchEvery":                                          "stub:homeCheck,t.Setenv",
	"TestWebInstall":                                          "stub:goos,stubLookPath,stubServices",
	"TestWebInstallAs":                                        "stubServices",
	"TestWebInstallHost":                                      "stubServices",
	"TestWebServeAsRecordsItself":                             "statusYard",
	"TestWebServeAsRefused":                                   "statusYard",
	"TestWebServeAsTakesFlagEnvAndSetting":                    "statusYard,t.Setenv",
	"TestWebServeClosesViewIfPagesAddrIsInUse":                "statusYard",
	"TestWebServeJWTModeServes":                               "statusYard",
	"TestWebServeJWTRefusals":                                 "statusYard,t.Setenv",
	"TestWebServeRecordsItself":                               "statusYard",
	"TestWebStart":                                            "stub:answers,stub:spawn,stub:startWait,stubWeb",
	"TestWebStartAs":                                          "stub:answers,stub:spawn",
	"TestWebStartHost":                                        "stub:answers,stub:spawn",
	"TestWebStop":                                             "running,stubWeb",
	"TestWhatNoAnswerMeans":                                   "stub:tryTimeout",
	"TestWorktreeLeavesTheBeadsOwnEnvironmentAlone":           "t.Chdir",
	"TestYardLeadsStandInLogin":                               "fakeTools,noHome,stub:tryHarnessCall",
	"TestWorktreeNamesTheSessionWhenTheEnvironmentIsAnothers": "t.Chdir",
	"TestYardrAt":                                             "noHome",
}

// parFacts is what a function does that decides parallelism: the
// package-level helpers it calls, the process and package state it
// touches, and whether it claims parallelism itself.
type parFacts struct {
	calls    map[string]bool // package-level idents called
	sels     map[string]bool // selector names called (Setenv, Unsetenv, Chdir, ...)
	assigns  map[string]bool // package vars assigned with =
	stubVars map[string]bool // &ident passed to stub(
	hasPar   bool            // the function calls t.Parallel()
}

func (f *parFacts) union(g parFacts) {
	f.hasPar = f.hasPar || g.hasPar
	for k := range g.calls {
		f.calls[k] = true
	}
	for k := range g.sels {
		f.sels[k] = true
	}
	for k := range g.assigns {
		f.assigns[k] = true
	}
	for k := range g.stubVars {
		f.stubVars[k] = true
	}
}

// parStateRoot is the package variable an assignment target writes through,
// including a field, map entry or pointer rooted at that variable. locals
// distinguishes a local that shadows a package variable with the same name.
func parStateRoot(expr ast.Expr, pkgVars map[string]bool, locals map[*ast.Ident]bool) string {
	switch e := expr.(type) {
	case *ast.Ident:
		if e.Name != "_" && pkgVars[e.Name] && !locals[e] {
			return e.Name
		}
	case *ast.SelectorExpr:
		return parStateRoot(e.X, pkgVars, locals)
	case *ast.IndexExpr:
		return parStateRoot(e.X, pkgVars, locals)
	case *ast.SliceExpr:
		return parStateRoot(e.X, pkgVars, locals)
	case *ast.StarExpr:
		return parStateRoot(e.X, pkgVars, locals)
	case *ast.ParenExpr:
		return parStateRoot(e.X, pkgVars, locals)
	}
	return ""
}

// parScope is the names one block, or the parameters of one function, have
// declared so far.
type parScope struct {
	names  map[string]token.Pos
	parent *parScope
}

func newParScope(parent *parScope) *parScope {
	return &parScope{names: map[string]token.Pos{}, parent: parent}
}

// declared is whether name has entered scope here or in a scope around it.
func (s *parScope) declared(name string, at token.Pos) bool {
	for ; s != nil; s = s.parent {
		if from, ok := s.names[name]; ok && at >= from {
			return true
		}
	}
	return false
}

// declare records where a name enters scope. A short declaration can reuse
// a name already declared in this block, so keep the earlier binding.
func (s *parScope) declare(name string, from token.Pos) {
	if old, ok := s.names[name]; !ok || from < old {
		s.names[name] = from
	}
}

// fields declares the names of parameter, result and receiver lists.
func (s *parScope) fields(lists ...*ast.FieldList) {
	for _, l := range lists {
		if l == nil {
			continue
		}
		for _, f := range l.List {
			for _, n := range f.Names {
				s.declare(n.Name, token.NoPos)
			}
		}
	}
}

// parLocals are the identifiers of fn that name something fn declares, a
// parameter or a variable of a block before them, and so shadow a package
// variable of the same name where they stand. go/ast once resolved them
// (the Obj of an identifier), and deprecates that, since without types it
// cannot always be right, as for the keys of a struct literal. The root of
// an assignment target is plain lexical scoping, which the syntax settles:
// read in source order, a name is declared for what follows it and for no
// more than its block.
func parLocals(fn *ast.FuncDecl) map[*ast.Ident]bool {
	root := newParScope(nil)
	root.fields(fn.Recv, fn.Type.Params, fn.Type.Results)
	r := parResolver{scope: root, locals: map[*ast.Ident]bool{}}
	ast.Walk(r, fn.Body)
	return r.locals
}

// parResolver is the visitor parLocals walks with: the scope of the node it
// is visiting, and the identifiers found local so far.
type parResolver struct {
	scope  *parScope
	locals map[*ast.Ident]bool
}

func (r parResolver) Visit(n ast.Node) ast.Visitor {
	switch n := n.(type) {
	case nil:
		return nil
	case *ast.FuncLit:
		r.scope = newParScope(r.scope)
		r.scope.fields(n.Type.Params, n.Type.Results)
	case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.CaseClause, *ast.CommClause:
		r.scope = newParScope(r.scope)
	}
	switch n := n.(type) {
	case *ast.AssignStmt:
		if n.Tok == token.DEFINE {
			r.define(n.End(), n.Lhs...)
		}
	case *ast.RangeStmt:
		if n.Tok == token.DEFINE {
			r.define(n.X.End(), n.Key, n.Value)
		}
	case *ast.DeclStmt:
		if g, ok := n.Decl.(*ast.GenDecl); ok && (g.Tok == token.VAR || g.Tok == token.CONST) {
			for _, spec := range g.Specs {
				if v, ok := spec.(*ast.ValueSpec); ok {
					for _, id := range v.Names {
						r.scope.declare(id.Name, v.End())
					}
				}
			}
		}
	case *ast.Ident:
		if r.scope.declared(n.Name, n.Pos()) {
			r.locals[n] = true
		}
	}
	return r
}

// define declares the identifiers among lhs when the declaration ends, as
// := does: its names do not shadow an outer one in their initializer.
func (r parResolver) define(from token.Pos, lhs ...ast.Expr) {
	for _, e := range lhs {
		if id, ok := e.(*ast.Ident); ok {
			r.scope.declare(id.Name, from)
		}
	}
}

// parMarked reports whether f touches process or package state: the
// environment, the working directory, the process umask, or a package
// variable.
func parMarked(f parFacts) bool {
	return parEnvironmentFact(f) || f.sels["Chdir"] || f.sels["Umask"] || len(f.assigns) > 0 || len(f.stubVars) > 0
}

func parEnvironmentFact(f parFacts) bool {
	return f.sels["Setenv"] || f.sels["Unsetenv"] || f.sels["Clearenv"]
}

// parCollect reads fn's own facts. A method sharing a helper's name merges
// with it at the caller's, so the reachability stays conservative.
func parCollect(fn *ast.FuncDecl, pkgVars map[string]bool) parFacts {
	f := parFacts{calls: map[string]bool{}, sels: map[string]bool{}, assigns: map[string]bool{}, stubVars: map[string]bool{}}
	locals := parLocals(fn)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			switch fun := n.Fun.(type) {
			case *ast.Ident:
				f.calls[fun.Name] = true
				if fun.Name == "stub" && len(n.Args) >= 2 {
					if u, ok := n.Args[1].(*ast.UnaryExpr); ok && u.Op == token.AND {
						if name := parStateRoot(u.X, pkgVars, locals); name != "" {
							f.stubVars[name] = true
						}
					}
				}
			case *ast.SelectorExpr:
				f.sels[fun.Sel.Name] = true
				if fun.Sel.Name == "Parallel" {
					f.hasPar = true
				}
			}
		case *ast.AssignStmt:
			if n.Tok != token.ASSIGN {
				return true
			}
			for _, lhs := range n.Lhs {
				if name := parStateRoot(lhs, pkgVars, locals); name != "" {
					f.assigns[name] = true
				}
			}
		case *ast.RangeStmt:
			if n.Tok != token.ASSIGN {
				return true
			}
			for _, lhs := range []ast.Expr{n.Key, n.Value} {
				if name := parStateRoot(lhs, pkgVars, locals); name != "" {
					f.assigns[name] = true
				}
			}
		case *ast.IncDecStmt:
			if name := parStateRoot(n.X, pkgVars, locals); name != "" {
				f.assigns[name] = true
			}
		}
		return true
	})
	return f
}

// parReach is every function's transitive facts: its own unioned with what
// the helpers it calls reach.
func parReach(own map[string]parFacts) map[string]parFacts {
	reached := map[string]parFacts{}
	for name := range own {
		out := parFacts{calls: map[string]bool{}, sels: map[string]bool{}, assigns: map[string]bool{}, stubVars: map[string]bool{}}
		seen := map[string]bool{}
		var visit func(string)
		visit = func(name string) {
			if seen[name] {
				return
			}
			seen[name] = true
			f, ok := own[name]
			if !ok {
				return
			}
			out.union(f)
			for c := range f.calls {
				visit(c)
			}
		}
		visit(name)
		reached[name] = out
	}
	return reached
}

// parWitness is a shortest call path from test to a function whose own
// facts hold marker (a selector, assign:var or stub:var), for the error.
func parWitness(test, marker string, own map[string]parFacts) string {
	holds := func(f parFacts) bool {
		switch {
		case marker == "Chdir" || marker == "Umask" || marker == "Setenv" || marker == "Unsetenv" || marker == "Clearenv":
			return f.sels[marker]
		case strings.HasPrefix(marker, "assign:"):
			return f.assigns[strings.TrimPrefix(marker, "assign:")]
		default:
			return f.stubVars[strings.TrimPrefix(marker, "stub:")]
		}
	}
	prev := map[string]string{test: ""}
	queue := []string{test}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if holds(own[cur]) {
			path := []string{cur}
			for prev[path[0]] != "" {
				path = append([]string{prev[path[0]]}, path...)
			}
			if len(path) == 1 {
				return "in its own body"
			}
			return "via " + strings.Join(path[1:], " -> ")
		}
		for c := range own[cur].calls {
			if _, ok := own[c]; !ok {
				continue
			}
			if _, seen := prev[c]; !seen {
				prev[c] = cur
				queue = append(queue, c)
			}
		}
	}
	return "through an unknown path"
}

// parMarkers names what r touches, for the error: the environment, the
// directory, the umask, or the package variable.
func parMarkers(r parFacts) []string {
	var out []string
	for _, marker := range []string{"Clearenv", "Setenv", "Unsetenv"} {
		if r.sels[marker] {
			out = append(out, marker)
		}
	}
	if r.sels["Chdir"] {
		out = append(out, "Chdir")
	}
	if r.sels["Umask"] {
		out = append(out, "Umask")
	}
	for v := range r.assigns {
		out = append(out, "assign:"+v)
	}
	for v := range r.stubVars {
		out = append(out, "stub:"+v)
	}
	slices.Sort(out)
	return out
}

// parCheck enforces the section 3.3 rule over the package's tests: a test
// may carry t.Parallel only when it neither assigns a package variable nor
// calls a helper that does, and touches neither the process environment,
// the process umask, nor the working directory. A test that stays
// sequential must be listed in allow with its seam, which must itself
// reach process or package state. It returns a violation per breach.
func parCheck(own map[string]parFacts, allow map[string]string) []string {
	var tests []string
	for name := range own {
		if strings.HasPrefix(name, "Test") && name != "TestMain" {
			tests = append(tests, name)
		}
	}
	slices.Sort(tests)
	reached := parReach(own)
	var bad []string
	for _, name := range tests {
		r := reached[name]
		if r.hasPar {
			for _, m := range parMarkers(r) {
				what := map[string]string{
					"Setenv":   "the process environment (t.Setenv or os.Setenv)",
					"Unsetenv": "the process environment (os.Unsetenv)",
					"Clearenv": "the process environment (os.Clearenv)",
					"Chdir":    "the working directory (t.Chdir or os.Chdir)",
					"Umask":    "the process umask (syscall.Umask)",
				}[m]
				if what == "" {
					what = "package state (" + m + ")"
				}
				bad = append(bad, fmt.Sprintf("%s calls t.Parallel but reaches %s %s", name, what, parWitness(name, m, own)))
			}
			continue
		}
		seam, ok := allow[name]
		if !ok {
			via := []string{}
			for _, marker := range []string{"Setenv", "Unsetenv", "Clearenv"} {
				if own[name].sels[marker] {
					via = append(via, marker)
				}
			}
			if own[name].sels["Chdir"] {
				via = append(via, "t.Chdir")
			}
			for v := range own[name].assigns {
				via = append(via, "assign:"+v)
			}
			for v := range own[name].stubVars {
				via = append(via, "stub:"+v)
			}
			for c := range own[name].calls {
				if parMarked(reached[c]) {
					via = append(via, c)
				}
			}
			slices.Sort(via)
			hint := ""
			if len(via) > 0 {
				hint = fmt.Sprintf(" (reaches %s)", strings.Join(via, ", "))
			}
			bad = append(bad, fmt.Sprintf("%s calls no t.Parallel and is not listed in allowSerial%s: parallelise it, or list it with its seam", name, hint))
			continue
		}
		if strings.TrimSpace(seam) == "" {
			bad = append(bad, fmt.Sprintf("allowSerial lists %s without naming its seam", name))
		}
	}
	for name := range allow {
		_, ok := own[name]
		if !ok {
			bad = append(bad, fmt.Sprintf("allowSerial lists %s, which is no test any more: delete the entry", name))
			continue
		}
		if reached[name].hasPar {
			bad = append(bad, fmt.Sprintf("allowSerial lists %s, which calls t.Parallel now: delete the entry", name))
		}
		if !parMarked(reached[name]) {
			bad = append(bad, fmt.Sprintf("allowSerial lists %s, which reaches no process or package state: parallelise it and delete the entry", name))
		}
	}
	return bad
}

// Only the tests allowSerial names stay sequential; every other test runs
// beside the rest (design yardr-14tr, section 3.3). The gate reads the
// syntax the way note 6084's classifier did: a test that merely calls a
// helper which stubs is just as pinned as one that stubs itself.
// parLoad parses sources (file name to contents) and reads every test
// function's facts: package vars from every file first, facts second, so
// no file is read before its vars are known, however the files sort.
// Parsing and collecting both run file by file beside one another: the
// package holds hundreds of files, and reading them one after another
// costs this test over a second. Every merge is a union under one lock,
// so the facts are the same however the files finish.
func parLoad(sources map[string][]byte) (map[string]parFacts, error) {
	fset := token.NewFileSet()
	parsed := map[string]*ast.File{}
	var mu sync.Mutex
	var firstErr error
	var firstFile string
	var wg sync.WaitGroup
	for name, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil || name < firstFile {
					firstErr, firstFile = err, name
				}
				return
			}
			parsed[name] = f
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	// Package vars live in test files too (strayKill, yardEnvs), and
	// assigning one pins the test just the same.
	pkgVars := map[string]bool{}
	for _, f := range parsed {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, s := range g.Specs {
				if v, ok := s.(*ast.ValueSpec); ok {
					for _, n := range v.Names {
						pkgVars[n.Name] = true
					}
				}
			}
		}
	}
	own := map[string]parFacts{}
	for file, f := range parsed {
		if !strings.HasSuffix(file, "_test.go") {
			continue
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				got := parCollect(fd, pkgVars)
				mu.Lock()
				defer mu.Unlock()
				if old, ok := own[fd.Name.Name]; ok {
					old.union(got)
					got = old
				}
				own[fd.Name.Name] = got
			}()
		}
	}
	wg.Wait()
	return own, nil
}

func TestOnlyAllowedTestsStaySerial(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string][]byte{}
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		sources[file] = src
	}
	own, err := parLoad(sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range parCheck(own, allowSerial) {
		t.Error(v)
	}
}

// The rule's own check is checked: a parallel test reaching a stub
// through a helper is caught with its path, an unlisted sequential test
// is caught with what it reaches, and a stale allow entry is caught, as
// is a listed test reaching no marker; a listed test reaching only the
// umask passes. The var file sorts after the test file, so the check reads
// vars before facts however the files sort.
func TestParCheckFindsViolations(t *testing.T) {
	t.Parallel()
	own, err := parLoad(map[string][]byte{
		"z.go": []byte("package x\n\nvar seam = 1\nvar state = struct{ n int }{}\nvar stateMap = map[string]bool{}\n"),
		"a_test.go": []byte(`package x

import (
	"os"
	"syscall"
	"testing"
)

func stubIt(t *testing.T) { t.Setenv("K", "v") }
func parallelIt(t *testing.T) { t.Parallel() }
func first() { second() }
func second() { first(); state.n++ }

func TestParallelBad(t *testing.T) {
	t.Parallel()
	stubIt(t)
}

func TestParallelUnsetenvBad(t *testing.T) {
	t.Parallel()
	os.Unsetenv("K")
}

func TestParallelBadViaHelper(t *testing.T) {
	parallelIt(t)
	state.n++
}

func TestNestedParallelBad(t *testing.T) {
	t.Run("child", func(t *testing.T) {
		t.Parallel()
		stateMap["x"] = true
	})
}

func TestParallelLocalShadow(t *testing.T) {
	t.Parallel()
	state := struct{ n int }{}
	state.n++
}

func TestParallelNestedShadowDoesNotHidePackageWrite(t *testing.T) {
	t.Parallel()
	t.Run("child", func(t *testing.T) {
		state := struct{ n int }{}
		state.n++
	})
	state.n++
}

func shadowedParam(state *struct{ n int }) { state.n++ }

func TestParallelOtherShadows(t *testing.T) {
	t.Parallel()
	shadowedParam(nil)
	if state := 1; state > 0 {
		state++
	}
	var seam int
	seam++
	for stateMap := range 3 {
		stateMap++
	}
	func(stateMap map[string]bool) { stateMap["x"] = true }(nil)
}

func TestParallelShortInitializer(t *testing.T) {
	t.Parallel()
	state := func() { state.n++ }
	state()
}

func TestParallelVarInitializer(t *testing.T) {
	t.Parallel()
	var state = func() { state.n++ }
	state()
}

func TestParallelWriteBeforeShadow(t *testing.T) {
	t.Parallel()
	state.n++
	state := struct{ n int }{}
	_ = state
}

func TestParallelWriteAfterShadowScope(t *testing.T) {
	t.Parallel()
	if state := 1; state > 0 {
		state++
	}
	state.n++
}

func TestSerialOK(t *testing.T) { stubIt(t) }

func TestParallelOK(t *testing.T) { t.Parallel() }

func TestSerialUnlisted(t *testing.T) { stubIt(t); seam = 2 }

func TestSerialCycleUnlisted(t *testing.T) { first() }

func TestSerialUmaskOK(t *testing.T) { syscall.Umask(0) }

func TestSerialUnmarked(t *testing.T) {}
`),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := parCheck(own, map[string]string{"TestSerialOK": "", "TestGone": "stale",
		"TestSerialUmaskOK": "syscall.Umask", "TestSerialUnmarked": "stale seam"})
	want := []string{
		"TestNestedParallelBad calls t.Parallel but reaches package state (assign:stateMap) in its own body",
		"TestParallelBad calls t.Parallel but reaches the process environment (t.Setenv or os.Setenv) via stubIt",
		"TestParallelBadViaHelper calls t.Parallel but reaches package state (assign:state) in its own body",
		"TestParallelNestedShadowDoesNotHidePackageWrite calls t.Parallel but reaches package state (assign:state) in its own body",
		"TestParallelShortInitializer calls t.Parallel but reaches package state (assign:state) in its own body",
		"TestParallelUnsetenvBad calls t.Parallel but reaches the process environment (os.Unsetenv) in its own body",
		"TestParallelVarInitializer calls t.Parallel but reaches package state (assign:state) in its own body",
		"TestParallelWriteAfterShadowScope calls t.Parallel but reaches package state (assign:state) in its own body",
		"TestParallelWriteBeforeShadow calls t.Parallel but reaches package state (assign:state) in its own body",
		"TestSerialCycleUnlisted calls no t.Parallel and is not listed in allowSerial (reaches first): parallelise it, or list it with its seam",
		"TestSerialUnlisted calls no t.Parallel and is not listed in allowSerial (reaches assign:seam, stubIt): parallelise it, or list it with its seam",
		"allowSerial lists TestSerialOK without naming its seam",
		"allowSerial lists TestGone, which is no test any more: delete the entry",
		"allowSerial lists TestSerialUnmarked, which reaches no process or package state: parallelise it and delete the entry",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("violations =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
