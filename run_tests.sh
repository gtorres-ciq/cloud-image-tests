#!/bin/bash

X86_SHAPES=(
    "n1-standard-4"
    "n2-standard-4"
    "c3-standard-4"
    "c4-standard-4"
    "c3d-standard-4"
    "c4d-standard-4"
    "n4d-standard-2"
    "n4-highcpu-2"
    "c3-highcpu-4"
    "c4-highcpu-4"
    "n2-highcpu-2"
    "c3-standard-192-metal"
)

ARM_SHAPES=(
    "t2a-standard-2"
    "c4a-standard-4"
    "n4a-standard-2"
)

X86_IMAGES=(
    "rocky-linux-8"
    "rocky-linux-9"
    "rocky-linux-10"
    "rocky-linux-8-optimized-gcp"
    "rocky-linux-9-optimized-gcp"
    "rocky-linux-10-optimized-gcp"
    "rocky-linux-8-optimized-gcp-nvidia-580"
    "rocky-linux-9-optimized-gcp-nvidia-580"
    "rocky-linux-10-optimized-gcp-nvidia-580"
    "rocky-linux-8-optimized-gcp-nvidia-latest"
    "rocky-linux-9-optimized-gcp-nvidia-latest"
    "rocky-linux-10-optimized-gcp-nvidia-latest"
)

ARM_IMAGES=(
    "rocky-linux-8-arm64"
    "rocky-linux-9-arm64"
    "rocky-linux-10-arm64"
    "rocky-linux-8-optimized-gcp-arm64"
    "rocky-linux-9-optimized-gcp-arm64"
    "rocky-linux-10-optimized-gcp-arm64"
    "rocky-linux-8-optimized-gcp-nvidia-580-arm64"
    "rocky-linux-9-optimized-gcp-nvidia-580-arm64"
    "rocky-linux-10-optimized-gcp-nvidia-580-arm64"
    "rocky-linux-8-optimized-gcp-nvidia-latest-arm64"
    "rocky-linux-9-optimized-gcp-nvidia-latest-arm64"
    "rocky-linux-10-optimized-gcp-nvidia-latest-arm64"
)

TESTS=(
    "cvm"
    "disk"
    "guestagent"
    "hostnamevalidation"
    "hotattach"
    "imageboot"
    "licensevalidation"
    "livemigrate"
    "loadbalancer"
    "lssd"
    "metadata"
    "network"
    "packagevalidation"
    "security"
    "ssh"
    "suspendresume"
    "vmspec"
)

REGIONS=(
    "europe-west1-b"
    "europe-west1-c"
    "europe-west1-d"
    "europe-west4-a"
    "europe-west4-b"
    "europe-west4-c"
    "asia-southeast1-a"
    "asia-southeast1-b"
    "asia-southeast1-c"
    "us-central1-c"
)

PROJECT=ciq-test-servers

# Create help function that shows usage
function show_help() {
    echo "Usage: $0 [--arm] [--shapes <shape1,shape2,...>] [--images <image1,image2,...>] [--tests <test1,test2,...>] [--regions <region1,region2,...>] [--parallel <parallel test count>] [--help|-h] [...]"
    echo ""
    echo "Options:"
    echo "  --arm                            Run tests on ARM shapes and images"
    echo "  --shapes <shape1,shape2,...>     Comma-separated list of shapes to test (overrides default shapes)"
    echo "  --images <image1,image2,...>     Comma-separated list of images to test (overrides default images)"
    echo "  --tests <test1,test2,...>        Comma-separated list of tests to run (overrides default tests)"
    echo "  --regions <region1,region2,...>  Specify the region to run tests in (overrides default regions)"
    echo "  --parallel <count>               Number of tests to run in parallel (default: $PARALLEL_RUN_COUNT)"
    echo "  --project <project>              GCP project to use (default: $PROJECT)"
    echo "  --help, -h                       Show this help message"
    echo "  [...]                            Other options are passed to the cloud-image-tests command in the container"
    echo ""
    echo "Default x86 shapes: ${X86_SHAPES[*]}"
    echo "Default ARM shapes: ${ARM_SHAPES[*]}"
    echo "Default x86 images: ${X86_IMAGES[*]}"
    echo "Default ARM images: ${ARM_IMAGES[*]}"
    echo "Default tests: ${TESTS[*]}"
    echo "Default regions: ${REGIONS[*]}"
}

SHAPES=("${X86_SHAPES[@]}")
IMAGES=("${X86_IMAGES[@]}")
SHAPE_ARG="-x86_shape"
PARALLEL_RUN_COUNT=20
USER_PROVIDED_REGIONS=false

# parse arguments to override which shapes and images to test
while [[ $# -gt 0 ]]; do
    case $1 in
        --arm)
            SHAPES=("${ARM_SHAPES[@]}")
            IMAGES=("${ARM_IMAGES[@]}")
            SHAPE_ARG="-arm64_shape"
            shift
            ;;
        --regions)
            shift
            IFS=',' read -r -a MAN_REGIONS <<< "$1"
            USER_PROVIDED_REGIONS=true
            shift
            ;;
        --shapes)
            shift
            IFS=',' read -r -a MAN_SHAPES <<< "$1"
            shift
            ;;
        --images)
            shift
            IFS=',' read -r -a MAN_IMAGES <<< "$1"
            shift
            ;;
        --tests)
            shift
            IFS=',' read -r -a TESTS <<< "$1"
            shift
            ;;
        --parallel)
            shift
            if [[ -n "$1" ]]; then
                PARALLEL_RUN_COUNT="$1"
                shift
            else
                echo "Error: --parallel requires a value."
                exit 1
            fi
            ;;
        --project)
            shift
            if [[ -n "$1" ]]; then
                PROJECT="$1"
                shift
            else
                echo "Error: --project requires a value."
                exit 1
            fi
            ;;
        --help|-h)
            show_help
            exit 0
            ;;
        *)
            EXTRA_ARGS+=("$1")
            shift
            if [[ $# -gt 0 && ! "$1" == --* ]]; then
                EXTRA_ARGS+=("$1")
                shift
            fi
            ;;
    esac
done

# If user provided shapes, override the default shapes
if [ "${#MAN_SHAPES[@]}" -gt 0 ]; then
    unset SHAPES
    SHAPES=("${MAN_SHAPES[@]}")
fi
# If user provided images, override the default images
if [ "${#MAN_IMAGES[@]}" -gt 0 ]; then
    unset IMAGES
    IMAGES=("${MAN_IMAGES[@]}")
fi
# If user provided regions, override the default regions
if [ "${#MAN_REGIONS[@]}" -gt 0 ]; then
    unset REGIONS
    REGIONS=("${MAN_REGIONS[@]}")
fi

echo "Running tests with the following configuration:"
echo "Shapes: ${SHAPES[*]}"
echo "Images: ${IMAGES[*]}"
echo "Tests: ${TESTS[*]}"
echo "Region: ${REGIONS[*]}"
echo "Shape argument: $SHAPE_ARG"
echo "Project: $PROJECT"

retval=0
for shape in "${SHAPES[@]}"; do
    CHECK_IMAGES=("${IMAGES[@]}")
    CHECK_REGIONS=("${REGIONS[@]}")
    CHECK_TESTS=("${TESTS[@]}")
    # If shape doesn't start with t2a or c3d, skip disk test
    if [[ ! "$shape" == t2a-standard* && ! "$shape" == c3d-standard* ]]; then
        for i in "${!CHECK_TESTS[@]}"; do
            if [[ ${CHECK_TESTS[i]} = "disk" ]]; then
                unset 'CHECK_TESTS[i]'
            fi
        done
    fi
    # If shape doesn't start with c3, skip lssd test
    if [[ ! "$shape" == c3-standard* ]]; then
        for i in "${!CHECK_TESTS[@]}"; do
            if [[ ${CHECK_TESTS[i]} = "lssd" ]]; then
                unset 'CHECK_TESTS[i]'
            fi
        done
    fi
    # If shape doesn't start with n2d or c3, skip cvm test
    if [[ ! "$shape" == n2d-standard* && ! "$shape" == c3-standard* ]]; then
        for i in "${!CHECK_TESTS[@]}"; do
            if [[ ${CHECK_TESTS[i]} = "cvm" ]]; then
                unset 'CHECK_TESTS[i]'
            fi
        done
    fi
    # If shape doesn't start with n1, n2 or n2d, skip vmspec test
    if [[ ! "$shape" == n1-standard* && ! "$shape" == n2-standard* && ! "$shape" == n2d-standard* ]]; then
        for i in "${!CHECK_TESTS[@]}"; do
            if [[ ${CHECK_TESTS[i]} = "vmspec" ]]; then
                unset 'CHECK_TESTS[i]'
            fi
        done
    fi
    # If shape is c4a metal, remove all Rocky 8 images, set region to us-central-b and us-central1-f,
    # and remove tests that are known to fail or not applicable on this shape:
    if [ "$shape" == "c4a-highmem-96-metal" ]; then
        for i in "${!CHECK_IMAGES[@]}"; do
            if [[ ${CHECK_IMAGES[i]} == rocky-linux-8* ]]; then
                unset 'CHECK_IMAGES[i]'
            fi
        done
        CHECK_REGIONS=("us-central1-b")
        keep=(suspendresume licensevalidation loadbalancer metadata packagevalidation)
        for target in "${CHECK_TESTS[@]}"; do
            if [[ ! " ${keep[*]} " =~ " ${target} " ]]; then
                for i in "${!CHECK_TESTS[@]}"; do
                    if [[ ${CHECK_TESTS[i]} = $target ]]; then
                        unset 'CHECK_TESTS[i]'
                    fi
                done
            fi
        done
    fi
    # If shape == c3-standard-192-metal, limit region to europe-west1-b
    if [ "$shape" == "c3-standard-192-metal" ]; then
        #CHECK_REGIONS=("europe-west1-c", "europe-west1-b")
        CHECK_REGIONS=("europe-west1-c")
        # Remove tests that are known to fail or not applicable on this shape:
        #  * cvm - Switches to a different instance type, so not applicable
        #  * livemigrate - Can't migrate a metal instance
        #  * suspendresume - Can't suspend a metal instance?
        #  * imageboot - Tests secureboot, which is not supported on metal and boot time which is very long on metal
        #  * network - This instance type can only have a single NIC, so network tests fail
        #  * disk - Switches to a different instance type, so not applicable
        #  * lssd - Switches to a different instance type, so not applicable
        #  * vmspec - Switches to a different instance type, so not applicable
        delete=(cvm livemigrate suspendresume imageboot network disk lssd vmspec)
        for target in "${delete[@]}"; do
            for i in "${!CHECK_TESTS[@]}"; do
                if [[ ${CHECK_TESTS[i]} = $target ]]; then
                    unset 'CHECK_TESTS[i]'
                fi
            done
        done
    elif [ "$shape" == "c4a-highmem-96-metal" ]; then
        CHECK_REGIONS=("us-central1-b")
        # Remove tests that are known to fail or not applicable on this shape:
        #  * cvm - Switches to a different instance type, so not applicable
        #  * livemigrate - Can't migrate a metal instance
        #  * suspendresume - Can't suspend a metal instance?
        #  * imageboot - Tests secureboot, which is not supported on metal and boot time which is very long on metal
        #  * network - This instance type can only have a single NIC, so network tests fail
        #  * disk - Switches to a different instance type, so not applicable
        #  * lssd - Switches to a different instance type, so not applicable
        #  * vmspec - Switches to a different instance type, so not applicable
        delete=(cvm disk lssd vmspec)
        for target in "${delete[@]}"; do
            for i in "${!CHECK_TESTS[@]}"; do
                if [[ ${CHECK_TESTS[i]} = $target ]]; then
                    unset 'CHECK_TESTS[i]'
                fi
            done
        done
    elif [[ "$shape" == n4a-standard* ]]; then
        if ! $USER_PROVIDED_REGIONS; then
            CHECK_REGIONS=("us-central1-b")
        fi
    elif [[ "$shape" == c4d-standard* ]]; then
        if ! $USER_PROVIDED_REGIONS; then
            for i in "${!CHECK_REGIONS[@]}"; do
                if [[ ${CHECK_REGIONS[i]} == "europe-west1-c" ]] || [[ ${CHECK_REGIONS[i]} == "europe-west1-d" ]] || [[ ${CHECK_REGIONS[i]} == "us-central1-c" ]]; then
                    unset 'CHECK_REGIONS[i]'
                fi
            done
        fi
    elif [[ "$shape" == n4d-standard* ]]; then
        if ! $USER_PROVIDED_REGIONS; then
            CHECK_REGIONS=("us-central1-a" "us-central1-b" "us-central1-c" "us-east1-b" "us-east1-d" "europe-west1-c" "europe-west4-a" "europe-west4-b")
        fi
    # If shape == t2a-standard-2, remove
    elif [[ "$shape" == t2a-standard* ]]; then
        if ! $USER_PROVIDED_REGIONS; then
            CHECK_REGIONS=("us-central1-a" "us-central1-b" "us-central1-f" "europe-west4-a" "europe-west4-b" "europe-west4-c" "asia-southeast1-b" "asia-southeast1-c")
        fi
    # There's never enough c4d capacity in europe-west4-b, so remove it from the list
    elif [[ "$shape" == c4d-standard* ]]; then
        if ! $USER_PROVIDED_REGIONS; then
            delete=(europe-west1-b europe-west1-c europe-west1-d europe-west4-b)
            for target in "${delete[@]}"; do
                for i in "${!CHECK_REGIONS[@]}"; do
                    if [[ ${CHECK_REGIONS[i]} = $target ]]; then
                        unset 'CHECK_REGIONS[i]'
                    fi
                done
            done
        fi
    fi
    if [[ "$shape" == n4-highcpu-* ]] || [[ "$shape" == c3-highcpu-* ]] || [[ "$shape" == c4-highcpu-* ]] || [[ "$shape" == n2-highcpu-* ]]; then
        # For highcpu shapes, limit tests to network, ssh, loadbalancer, metadata, and imageboot
        keep=(network ssh loadbalancer metadata imageboot)
        for target in "${CHECK_TESTS[@]}"; do
            if [[ ! " ${keep[*]} " =~ " ${target} " ]]; then
                for i in "${!CHECK_TESTS[@]}"; do
                    if [[ ${CHECK_TESTS[i]} = $target ]]; then
                        unset 'CHECK_TESTS[i]'
                    fi
                done
            fi
        done
    fi
    for image in "${CHECK_IMAGES[@]}"; do
        PCOUNT=${PARALLEL_RUN_COUNT}
        if [ "$shape" == "c3-standard-192-metal" ] || [ "$shape" == "c4a-highmem-96-metal" ]; then
            # For c3-standard-192-metal, run only one test at a time
            PCOUNT=1
        elif [[ "$shape" == n4a-standard* ]] && [[ "$PCOUNT" -gt 12 ]]; then
            PCOUNT=12
        fi
        for testrun in "${CHECK_TESTS[@]}"; do
            # For c4-standard* shapes, remove us-central1-* regions from hotattach tests due to known issues
            if ! $USER_PROVIDED_REGIONS && [[ "$shape" == c4-standard* ]] && [[ "$testrun" == "hotattach" ]]; then
                delete=(us-central1-a us-central1-b us-central1-c us-central1-f)
                for target in "${delete[@]}"; do
                    for i in "${!CHECK_REGIONS[@]}"; do
                        if [[ ${CHECK_REGIONS[i]} = $target ]]; then
                            unset 'CHECK_REGIONS[i]'
                        fi
                    done
                done
            fi
            # If image doesn't contain '/', prepend 'projects/gce-ciq-images/global/images/family/'
            if [[ "$image" != *"/"* ]]; then
                base_image="$image"
                image="projects/gce-ciq-images/global/images/family/$image"
            else
                base_image=$(basename "$image")
            fi
            if [ -s "${base_image}_${shape}_${testrun}.xml" ]; then
                # Check if there were any failures in the previous run
                grep -q "<failure mes" "${base_image}_${shape}_${testrun}.xml"
                if [ $? -eq 0 ]; then
                    echo "Test $testrun for image $image with shape $shape failed in previous run, re-running."
                else
                    # If the file exists and has no failures, skip this test
                    echo "Test $testrun for image $image with shape $shape already completed successfully, skipping."
                    continue
                fi
            fi
            REGION=$(echo "${CHECK_REGIONS[@]}" | tr ' ' ',')
            echo "Running test: $testrun for image: $image with shape: $shape"
            QUOTED_EXTRA_ARGS=""
            for arg in "${EXTRA_ARGS[@]}"; do
                printf -v escaped_arg '%q' "$arg"
                QUOTED_EXTRA_ARGS+=" $escaped_arg"
            done
            set -x
            /bin/bash -c "docker run --rm -v $(pwd):/curpath:z -v ~/.config/gcloud/:/creds:z -e GOOGLE_APPLICATION_CREDENTIALS=/creds/application_default_credentials.json cloud-image-tests --project $PROJECT --filter \"^($testrun)$\" --zones "$REGION" --images \"$image\" ${SHAPE_ARG}=\"$shape\" --parallel_count 1${QUOTED_EXTRA_ARGS} | tee \"${base_image}_${shape}_${testrun}.xml\"" &
            set +x
            # Shift REGIONS so first region is moved to the end of the array
            CHECK_REGIONS=("${CHECK_REGIONS[@]:1}" "${CHECK_REGIONS[0]}")
            # Capture the PID of the background job
            while /bin/true; do
                # Check the number of running jobs
                JOB_COUNT=$(jobs -r | wc -l)
                sleep 5
                if [ "$JOB_COUNT" -lt "$PCOUNT" ]; then
                    break
                fi
                echo "Waiting for jobs to finish, current count: $JOB_COUNT"
            done
            while [ -e /tmp/pause.txt ]; do
                JOB_COUNT=$(jobs -r | wc -l)
                echo "Waiting for pause.txt to be removed before running any new tests (current count: $JOB_COUNT)..."
                sleep 5
            done
        done
    done
done

while /bin/true; do
    # Check the number of running jobs
    JOB_COUNT=$(jobs -r | wc -l)
    if [ "$JOB_COUNT" -lt 1 ]; then
        break
    fi
    echo "Waiting for jobs to finish, current count: $JOB_COUNT"
    sleep 45
done

jobs -s

exit $retval
