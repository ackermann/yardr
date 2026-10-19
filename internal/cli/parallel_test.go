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
	"testing"
)

// allowSerial names every test that stays sequential on purpose: the test
// and the seam that keeps it there (design yardr-14tr, section 3.3). A
// test that is neither parallel nor listed fails the gate, so a future
// test cannot join the serial set silently; a listed test that turned
// parallel, or that no longer exists, fails it too.
var allowSerial = map[string]string{
	"TestAddrSettingsAreCheckedAsTheFlag":                   "newHome,stubWeb,t.Setenv",
	"TestBDPAndOTelStartStop":                               "running,stub:answers,stub:spawn,t.Setenv",
	"TestBareYardr":                                         "holdDaemon,setInteractive,stubAsk,stubFocus,stubLookPath,stubPane,stubStart",
	"TestBareYardrFinishesAYardWithoutDefaultKind":          "resumeYard,startsYardmaster,stubPane",
	"TestBareYardrLeavesOtherYardsAlone":                    "holdDaemon,resumeYard,setInteractive,startsYardmaster",
	"TestBareYardrStartsAYardThatDidNotStart":               "resumeYard,startsYardmaster,stubPane",
	"TestBeadListWithClosedIsCut":                           "stub:listLimit",
	"TestChatCommand":                                       "chatYard",
	"TestChatHookIsNotQuietBySilence":                       "stub:hookNow",
	"TestChatNeedsATerminal":                                "assign:interactive,chatYard",
	"TestChatWithoutCrew":                                   "chatYard",
	"TestCompactRefusesWhileTheServerRuns":                  "yardWithoutServer",
	"TestCompactWithoutAServer":                             "yardWithoutServer",
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
	"TestDoctorBlockerCycle":                                "fakeTools",
	"TestDoctorCodexTrust":                                  "fakeTools,t.Setenv",
	"TestDoctorCrewNotLoggedIn":                             "fakeTools",
	"TestDoctorEmptyHome":                                   "fakeTools",
	"TestDoctorFailingCrew":                                 "fakeTools",
	"TestDoctorFetchOnMac":                                  "fakeTools,newHome,stub:goarch,stub:goos,stub:loginShell,stub:userHomeDir",
	"TestDoctorFreshYard":                                   "fakeTools",
	"TestDoctorHarnessLabels":                               "fakeTools,newHome,stub:loginShell",
	"TestDoctorHerdrLoginService":                           "fakeTools,stubServices",
	"TestDoctorHerdrServer":                                 "fakeTools,stub:herdrServer",
	"TestDoctorKimiHooks":                                   "fakeTools,newHome,stub:userHomeDir",
	"TestDoctorLeavesAStoppedServerStopped":                 "fakeTools,holdDaemon,stub:connect",
	"TestDoctorLoginServiceDepots":                          "fakeTools,stub:goos,stub:loginService,stub:userHomeDir",
	"TestDoctorLoginShellFinds":                             "fakeTools,stub:loginShell,stub:loginShellTimeout",
	"TestDoctorLoginShellFindsHarnessForNewYard":            "fakeTools,stub:loginShell",
	"TestDoctorMissingDependency":                           "fakeTools",
	"TestDoctorNamesForeignFolderLogin":                     "t.Setenv",
	"TestDoctorNamesTheHerdrSession":                        "fakeTools,otherShell,stub:herdrServer",
	"TestDoctorNoFetchOfWhatIsFound":                        "fakeTools,newHome,stub:goarch,stub:goos,stub:herdrServer",
	"TestDoctorNoFetchOnLinux":                              "fakeTools,newHome,stub:goarch,stub:goos,stub:loginShell,stub:userHomeDir",
	"TestDoctorNotesQuietHook":                              "stub:answers,stub:hookNow",
	"TestDoctorOffers":                                      "fakeTools,newHome,stub:loginShell",
	"TestDoctorSaysWhatTheReceiverListensOn":                "fakeTools",
	"TestDoctorSetUpYard":                                   "fakeTools",
	"TestDoctorTry":                                         "fakeTools,stub:tryHarnessCall,stub:tryTimeout,t.Setenv",
	"TestDoctorUnreachableServer":                           "fakeTools",
	"TestDoctorVersionCheck":                                "fakeTools,stub:goos,stub:versionTimeout",
	"TestDoctorWebView":                                     "holdDaemon,statusYard,stubServices",
	"TestDoctorYardmasterGivenUpAfterSessionsGoneAtOnce":    "fakeTools",
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
	"TestFlowDrawWithoutAYard":                              "newHome",
	"TestHandOverAsSomeoneWhoIsNoUser":                      "setupYard,stubChat,stubFocus,stubPane",
	"TestHandOverReportsAViewThatFails":                     "setupYard,stub:runChat,stubPane",
	"TestHarnessListAndShow":                                "noHome",
	"TestHarnessLoginEndsInTheBrowser":                      "leadStandIn",
	"TestHarnessLoginFails":                                 "leadStandIn",
	"TestHarnessLoginGroupLandsInGroupFolder":               "leadStandInAs,t.Setenv",
	"TestHarnessLoginGroupRefusals":                         "fakeTools,noHome",
	"TestHarnessLoginInputGivenThroughSecretAsk":            "leadStandInAs",
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
	"TestHarnessOptions":                                    "stubLookPath",
	"TestHerdrLoginRejectsUnsafeSessionName":                "stubServices,t.Setenv",
	"TestHerdrServiceOfANamedSession":                       "stubServices",
	"TestHomeFlagInTheRefusal":                              "noHome,t.Setenv",
	"TestHomeFlagInstallsTheServiceForThatHome":             "noHome,stubLookPath,stubServices,t.Setenv",
	"TestHomeFlagOnboards":                                  "noHome,setInteractive,stubAsk,stubFocus,stubLookPath,stubPane,stubStart",
	"TestHomeFlagOrder":                                     "fakeTools,noHome,t.Setenv",
	"TestHomeFlagReachesChildren":                           "fakeTools,noHome,t.Chdir,t.Setenv",
	"TestHomeFlagTooLong":                                   "fakeTools,noHome",
	"TestHookAddMap":                                        "stateYard",
	"TestHookListShowsQuiet":                                "stub:hookNow",
	"TestInitAgainKeepsStyle":                               "styledYard",
	"TestInitFinishesAfterAFailedRun":                       "t.Setenv",
	"TestInitKeepsAYardmasterOfAnotherName":                 "newHome",
	"TestInitOperatorDefaultsToUser":                        "t.Setenv",
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
	"TestInitStyleFlags":                                    "styledYard",
	"TestInitSummaryNamesTheHome":                           "t.Setenv",
	"TestInitWithoutLoginName":                              "noLogin",
	"TestKillStrayLeavesAReusedPidAlone":                    "assign:strayDescribe,assign:strayKill",
	"TestLimitsGroupReadsTheGroupsLogin":                    "t.Setenv",
	"TestLoginNameNobodyHas":                                "chatYard",
	"TestLoginNotSupported":                                 "stubServices,upYard",
	"TestLoginShellPATH":                                    "stub:loginShell",
	"TestMailAUser":                                         "t.Chdir",
	"TestNoCommandReachesTheMachinesYard":                   "t.Setenv",
	"TestNoPairFormWithoutATerminal":                        "setInteractive,stubAsk",
	"TestNoQuestionWithoutACallOrAStart":                    "newAsker,setInteractive,stubLookPath",
	"TestOTelInstall":                                       "stubServices",
	"TestOnboardingAfterAFailedRun":                         "resumeYard,startsYardmaster,stubPane",
	"TestOnboardingAsYardrActor":                            "onboardTo,stubChat,stubServices,stubWeb",
	"TestOnboardingAsksForTheServices":                      "onboardTo,stubServices,stubWeb",
	"TestOnboardingAsksTheHarnessFirst":                     "newAsker",
	"TestOnboardingAsksTheStyle":                            "newHome,onboardTo,stubServices,stubWeb",
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
	"TestPreflightSaysWhereToGet":                           "fakeTools,stubAsk",
	"TestPrimeSaysThePullRequestHeadTheWorktreeIsOn":        "newHome",
	"TestPrimesSayTheStyle":                                 "styledYard",
	"TestPrimesWithoutStyle":                                "styledYard",
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
	"TestServerStatusNoYard":                                  "newHome",
	"TestServiceNameCarriesTheHome":                           "stubServices",
	"TestServiceNameWithoutHome":                              "stubServices",
	"TestServiceNotSupported":                                 "stubServices",
	"TestSessionListLogin":                                    "newHome",
	"TestSessionListOriginator":                               "newHome",
	"TestSessionListRunning":                                  "newHome",
	"TestSessionListSaysUntilWhenAGroupIsHeldBack":            "newHome",
	"TestSessionListWithEndedIsCut":                           "newHome",
	"TestSetStateIsTheOperatorsToSet":                         "stateYard",
	"TestSettingsGetPrintsTheValueInEffect":                   "t.Setenv",
	"TestSettingsSetChangesThePrimes":                         "styledYard",
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
	"TestSourceKeyTyped":                                      "assign:answers",
	"TestSpeakingAsAGitHubApp":                                "newAppYard",
	"TestStartAndInstallTakeTheAddrSetting":                   "newHome,stub:answers,stub:spawn,stubServices,t.Setenv",
	"TestStartAndInstallTakeThePagesFlags":                    "newHome,stub:answers,stub:spawn,stubServices,t.Setenv",
	"TestStatusAndDoctorSayActsAsFromSetting":                 "statusYard",
	"TestStatusAndDoctorSayJWTFrom":                           "statusYard",
	"TestStatusBackups":                                       "statusYard",
	"TestStatusBackupsWhenServerIsStopped":                    "newHome,stub:connect,stub:serverRunning",
	"TestStatusHomeFlagOnNoDirectory":                         "newHome",
	"TestStatusLeavesAStoppedServerStopped":                   "holdDaemon,newHome,stub:connect,stub:machineAddr",
	"TestStatusNamesTheHerdrSession":                          "otherShell,statusYard",
	"TestStatusNoYard":                                        "newHome,setInteractive,stubAsk",
	"TestStatusRunningYard":                                   "holdDaemon,statusYard",
	"TestStatusSameBuild":                                     "statusYard",
	"TestStatusServerOnly":                                    "statusYard",
	"TestStatusStaleWebView":                                  "statusYard",
	"TestStatusUnreachableServer":                             "fakeTools,newHome,stub:serverRunning",
	"TestStatusWebViewAs":                                     "statusYard",
	"TestStatusWebViewBuild":                                  "statusYard,stub:viewRevision",
	"TestStatusWorkLine":                                      "statusYard",
	"TestStopSkipsAPidGoneBeforeItsSignal":                    "stub:signalStop",
	"TestStopWaits":                                           "stub:signalStop",
	"TestStoreSendsTheRunsSession":                            "assign:connect",
	"TestStyleRefused":                                        "newHome",
	"TestTheNextRunWaitsForAYardmasterThatWasNotUp":           "holdDaemon,resumeYard,startsYardmaster,stubPane",
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
	"TestUpFindsHerdrWithTheLoginShell":                       "stub:loginShell,stub:spawn,stub:spawnHerdr,stubLookPath,t.Setenv,upYard",
	"TestUpLoginInstallsAndStarts":                            "stubLogin,upYard",
	"TestUpLoginInstallsTheWebViewTheYardNames":               "stubLogin,upYard",
	"TestUpLoginLeavesARunningServer":                         "stubLogin,stubLookPath,upYard",
	"TestUpLoginLeavesAServiceThatIsNotYardrs":                "stubLogin,upYard",
	"TestUpLoginWritesTheLoginShellsPATH":                     "stub:loginShell,stubLogin,upYard",
	"TestUpReadsHistoryOnlyWhenThePromptLeftTheScreen":        "stub:paneHistory,stub:paneTail",
	"TestUpStartsTheWebViewTheYardNames":                      "stub:answers,stub:spawn,upYard",
	"TestUpStartsWhatDoesNotRun":                              "stub:serverRunning,upYard",
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
	"TestUpgradeCheckNoYard":                                  "newHome",
	"TestUpgradeCheckOldView":                                 "stubServer,upgradeYard",
	"TestUpgradeCheckServerDown":                              "stubServer,upgradeYard",
	"TestUpgradeCheckServerOtherBuild":                        "stubServer,upgradeYard",
	"TestUpgradeCheckSinceFlag":                               "stubServer,upgradeYard",
	"TestUpgradeCheckViewAnswersOn500":                        "stubServer,upgradeYard",
	"TestUpgradeCheckViewOtherBuildByHand":                    "stubServer,upgradeYard",
	"TestUpgradeCheckViewOtherBuildFails":                     "stub:webService,stubServer,upgradeYard",
	"TestUpgradeCheckViewServiceCurrent":                      "stub:webService,stubServer,upgradeYard",
	"TestUpgradeCoveredYards":                                 "stub:goos,stub:serviceDir",
	"TestUpgradeFromFlag":                                     "upgradeYard",
	"TestUpgradeFromFlagBeatsSetting":                         "upgradeYard",
	"TestUpgradeFromFlagOperatorOnly":                         "upgradeYard",
	"TestUpgradeHatch":                                        "stub:upgradeNotifySend",
	"TestUpgradeInstallTarget":                                "stub:upgradeExecutable,stub:upgradeGoEnv",
	"TestUpgradePollDeadlineCancelsCheck":                     "stub:upgradeRunInstalled",
	"TestUpgradePollFailure":                                  "stub:upgradeRunInstalled",
	"TestUpgradePollRunsInstalledBinary":                      "stub:upgradeRunInstalled",
	"TestUpgradeRestartViews":                                 "stub:serviceCtl,stubServices",
	"TestUpgradeRestartViewsRefused":                          "stub:serviceCtl,stubServices",
	"TestUpgradeRestoreOn":                                    "t.Setenv",
	"TestUpgradeWithoutCheck":                                 "newHome,upgradeYard",
	"TestUseLoginPATH":                                        "stub:loginShell,t.Setenv",
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
	"TestWorktreeNamesTheSessionWhenTheEnvironmentIsAnothers": "t.Chdir",
	"TestYardrAt":                                             "noHome",
}

// parFacts is what a function does that decides parallelism: the
// package-level helpers it calls, the process and package state it
// touches, and whether it claims parallelism itself.
type parFacts struct {
	calls    map[string]bool // package-level idents called
	sels     map[string]bool // selector names called (Setenv, Chdir, ...)
	assigns  map[string]bool // package vars assigned with =
	stubVars map[string]bool // &ident passed to stub(
	hasPar   bool            // a top-level t.Parallel() statement
}

func (f *parFacts) union(g parFacts) {
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

// parMarked reports whether f touches process or package state: the
// environment, the working directory, or a package variable.
func parMarked(f parFacts) bool {
	return f.sels["Setenv"] || f.sels["Chdir"] || len(f.assigns) > 0 || len(f.stubVars) > 0
}

// parCollect reads fn's own facts. A method sharing a helper's name merges
// with it at the caller's, so the reachability stays conservative.
func parCollect(fn *ast.FuncDecl, pkgVars map[string]bool) parFacts {
	f := parFacts{calls: map[string]bool{}, sels: map[string]bool{}, assigns: map[string]bool{}, stubVars: map[string]bool{}}
	for _, s := range fn.Body.List {
		es, ok := s.(*ast.ExprStmt)
		if !ok {
			continue
		}
		c, ok := es.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Parallel" {
			f.hasPar = true
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			switch fun := n.Fun.(type) {
			case *ast.Ident:
				f.calls[fun.Name] = true
				if fun.Name == "stub" && len(n.Args) >= 2 {
					if u, ok := n.Args[1].(*ast.UnaryExpr); ok && u.Op == token.AND {
						if id, ok := u.X.(*ast.Ident); ok {
							f.stubVars[id.Name] = true
						}
					}
				}
			case *ast.SelectorExpr:
				f.sels[fun.Sel.Name] = true
			}
		case *ast.AssignStmt:
			if n.Tok != token.ASSIGN {
				return true
			}
			for _, lhs := range n.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" && pkgVars[id.Name] {
					f.assigns[id.Name] = true
				}
			}
		}
		return true
	})
	return f
}

// parReach is every function's transitive facts: its own unioned with what
// the helpers it calls reach.
func parReach(own map[string]parFacts) map[string]parFacts {
	memo := map[string]parFacts{}
	var reach func(string, map[string]bool) parFacts
	reach = func(name string, stack map[string]bool) parFacts {
		if m, ok := memo[name]; ok {
			return m
		}
		out := parFacts{calls: map[string]bool{}, sels: map[string]bool{}, assigns: map[string]bool{}, stubVars: map[string]bool{}}
		f, ok := own[name]
		if !ok || stack[name] {
			return out
		}
		stack[name] = true
		defer delete(stack, name)
		out.union(f)
		for c := range f.calls {
			out.union(reach(c, stack))
		}
		memo[name] = out
		return out
	}
	for name := range own {
		memo[name] = reach(name, map[string]bool{})
	}
	return memo
}

// parWitness is a shortest call path from test to a function whose own
// facts hold marker (a selector, assign:var or stub:var), for the error.
func parWitness(test, marker string, own map[string]parFacts) string {
	holds := func(f parFacts) bool {
		switch {
		case marker == "Setenv" || marker == "Chdir":
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
// directory, or the package variable.
func parMarkers(r parFacts) []string {
	var out []string
	if r.sels["Setenv"] {
		out = append(out, "Setenv")
	}
	if r.sels["Chdir"] {
		out = append(out, "Chdir")
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
// calls a helper that does, and touches neither the process environment
// nor the working directory. A test that stays sequential must be listed
// in allow with its seam. It returns a violation per breach.
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
		if own[name].hasPar {
			for _, m := range parMarkers(r) {
				what := map[string]string{"Setenv": "the process environment (t.Setenv)", "Chdir": "the working directory (t.Chdir)"}[m]
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
			if own[name].sels["Setenv"] {
				via = append(via, "t.Setenv")
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
		_ = seam
	}
	for name := range allow {
		f, ok := own[name]
		if !ok {
			bad = append(bad, fmt.Sprintf("allowSerial lists %s, which is no test any more: delete the entry", name))
			continue
		}
		if f.hasPar {
			bad = append(bad, fmt.Sprintf("allowSerial lists %s, which calls t.Parallel now: delete the entry", name))
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
func parLoad(sources map[string][]byte) (map[string]parFacts, error) {
	fset := token.NewFileSet()
	parsed := map[string]*ast.File{}
	for name, src := range sources {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			return nil, err
		}
		parsed[name] = f
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
			got := parCollect(fd, pkgVars)
			if old, ok := own[fd.Name.Name]; ok {
				old.union(got)
				got = old
			}
			own[fd.Name.Name] = got
		}
	}
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
// is caught with what it reaches, and a stale allow entry is caught. The
// var file sorts after the test file, so the check reads vars before
// facts however the files sort.
func TestParCheckFindsViolations(t *testing.T) {
	t.Parallel()
	own, err := parLoad(map[string][]byte{
		"z.go": []byte("package x\n\nvar seam = 1\n"),
		"a_test.go": []byte(`package x

import "testing"

func stubIt(t *testing.T) { t.Setenv("K", "v") }

func TestParallelBad(t *testing.T) {
	t.Parallel()
	stubIt(t)
}

func TestSerialOK(t *testing.T) { stubIt(t) }

func TestParallelOK(t *testing.T) { t.Parallel() }

func TestSerialUnlisted(t *testing.T) { stubIt(t); seam = 2 }
`),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := parCheck(own, map[string]string{"TestSerialOK": "stubIt", "TestGone": "stale"})
	want := []string{
		"TestParallelBad calls t.Parallel but reaches the process environment (t.Setenv) via stubIt",
		"TestSerialUnlisted calls no t.Parallel and is not listed in allowSerial (reaches assign:seam, stubIt): parallelise it, or list it with its seam",
		"allowSerial lists TestGone, which is no test any more: delete the entry",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("violations =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
