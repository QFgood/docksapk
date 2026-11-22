pluginManagement {
    repositories {
        google {
            content {
                includeGroupByRegex("com\\.android(\\..*)?")
                includeGroup("com.google.testing.platform")
                includeGroupByRegex("androidx(\\..*)?")
                includeGroupByRegex("com\\.google\\.android(\\..*)?")
            }
        }
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google {
            content {
                includeGroupByRegex("com\\.android(\\..*)?")
                includeGroup("com.google.testing.platform")
                includeGroupByRegex("androidx(\\..*)?")
                includeGroupByRegex("com\\.google\\.android(\\..*)?")
            }
        }
        mavenCentral()
    }
}
rootProject.name = "DocksAPK"
include(":app")
